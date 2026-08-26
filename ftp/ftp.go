// ftp/ftp.go - FTP filesystem driver
//
// This package implements the api.Driver interface for FTP/FTPS
// connections. It provides read/write access to remote FTP servers.
//
// Migrated from diskjockey-backend/disktypes/ftp.go

package ftp

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antimatter-studios/goftp"
	"github.com/christhomas/go-networkfs/pkg/api"
)

// Driver type ID - must match dispatcher registry
const DriverTypeID = 1

func init() {
	// Register this driver with the global registry
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &FTPDriver{}
	})
}

// FTPDriver implements the Driver interface for FTP connections
type FTPDriver struct {
	connected bool
	host      string
	port      int
	user      string
	pass      string
	rootPath  string
	ftps      bool
	client    *goftp.Client
	// Per-mount transport counters fed by api.CountingConn. The
	// host-side IOStatsCollector polls a Snapshot() each tick. For
	// FTPS we count *ciphertext* on the wire (the conn is wrapped
	// before the TLS layer) — that matches what an external network
	// monitor would see.
	stats *api.MountStats
}

// Name returns the driver identifier
func (d *FTPDriver) Name() string {
	return "ftp"
}

// Stats implements api.StatsProvider so the MountManager can hand our
// transport counters back through the C ABI.
func (d *FTPDriver) Stats() *api.MountStats { return d.stats }

// Mount establishes FTP connection.
//
// Config expects: host, port, user, pass, root, ftps.
//
// Errors are classified into human-readable categories so callers can
// tell apart "host is wrong" from "credentials are wrong" from "remote
// path doesn't exist" without parsing FTP server strings themselves.
// The mount-time rootPath check is load-bearing: without it, a bad
// root would only surface on the first listDir() call, which the
// FileProvider shell catches and turns back into a generic error.
func (d *FTPDriver) Mount(mountID int, config map[string]string) error {
	host := config["host"]
	if host == "" {
		return fmt.Errorf("missing required field 'host' in mount config")
	}

	user := config["user"]
	pass := config["pass"]
	rootPath := config["root"]
	if rootPath == "" {
		rootPath = "/"
	}

	portStr := config["port"]
	if portStr == "" {
		portStr = "21"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port %q: must be an integer", portStr)
	}

	ftps := config["ftps"] == "true" || config["ftps"] == "1"

	d.host = host
	d.port = port
	d.user = user
	d.pass = pass
	d.rootPath = rootPath
	d.ftps = ftps
	d.stats = &api.MountStats{}

	if err := d.connect(); err != nil {
		return classifyFTPConnectError(err, host, port, user)
	}

	if _, err := d.client.ReadDir(rootPath); err != nil {
		_ = d.client.Close()
		d.client = nil
		return classifyFTPPathError(err, rootPath)
	}

	d.connected = true
	return nil
}

// classifyFTPConnectError turns the raw dial/login error into a message
// that names the likely cause (bad host, refused port, timeout, TLS
// mismatch, bad credentials) instead of a generic "connection failed".
// The original error is kept at the end for the log.
func classifyFTPConnectError(err error, host string, port int, user string) error {
	if err == nil {
		return nil
	}
	if isFTPAuthError(err) {
		who := user
		if who == "" {
			who = "anonymous"
		}
		return fmt.Errorf("authentication failed for user %q (check username/password): %v", who, err)
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such host"),
		strings.Contains(msg, "server misbehaving"),
		strings.Contains(msg, "dns"):
		return fmt.Errorf("cannot resolve host %q (check the hostname): %v", host, err)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("%s:%d refused the connection (is the FTP server running on that port?): %v", host, port, err)
	case strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "deadline exceeded"):
		return fmt.Errorf("timed out connecting to %s:%d (check host/port/firewall): %v", host, port, err)
	case strings.Contains(msg, "no route to host"),
		strings.Contains(msg, "network is unreachable"):
		return fmt.Errorf("network unreachable for %s:%d: %v", host, port, err)
	case strings.Contains(msg, "connection reset"):
		return fmt.Errorf("%s:%d reset the connection (wrong port or TLS mismatch?): %v", host, port, err)
	case strings.Contains(msg, "tls"),
		strings.Contains(msg, "handshake"),
		strings.Contains(msg, "x509"):
		return fmt.Errorf("TLS handshake failed talking to %s:%d (check FTPS setting): %v", host, port, err)
	default:
		return fmt.Errorf("connection to %s:%d failed: %v", host, port, err)
	}
}

// ftpStatus reports the FTP reply code an error carries, or 0 when it
// carries none.
//
// The client exposes this through an interface rather than a concrete
// type, which is what lets the code below ask "what did the server
// actually say" without knowing how the error was built.
func ftpStatus(err error) int {
	if fe, ok := err.(goftp.Error); ok {
		return fe.Code()
	}
	return 0
}

// isFTPAuthError recognises the FTP auth-failed status codes.
// 530 = "Not logged in", 532 = "Need account for storing files".
func isFTPAuthError(err error) bool {
	if code := ftpStatus(err); code == 530 || code == 532 {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "530 ") ||
		strings.Contains(strings.ToLower(msg), "login incorrect")
}

// classifyFTPPathError maps the error from listing the remote root
// directory onto "path doesn't exist" / "permission denied" / generic.
func classifyFTPPathError(err error, path string) error {
	{
		switch ftpStatus(err) {
		case 550:
			return fmt.Errorf("remote path %q does not exist or is not accessible: %v", path, err)
		case 530:
			return fmt.Errorf("authentication required to access %q: %v", path, err)
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "550 ") {
		return fmt.Errorf("remote path %q does not exist or is not accessible: %v", path, err)
	}
	return fmt.Errorf("failed to access remote path %q: %v", path, err)
}

func (d *FTPDriver) connect() error {
	addr := fmt.Sprintf("%s:%d", d.host, d.port)

	// We always inject our own dial function so the returned net.Conn is
	// wrapped in api.CountingConn before the FTP library installs its
	// protocol layer on top. The library routes *every* connection
	// through it — control and data alike — so the counters see file
	// transfers and not merely the command channel.
	//
	// For FTPS the wrap happens before TLS, so the counts are of
	// ciphertext on the wire, which is what an external monitor would
	// observe.
	var tlsCfg *tls.Config
	if d.ftps {
		tlsCfg = &tls.Config{InsecureSkipVerify: true}
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	stats := d.stats

	cfg := goftp.Config{
		User:     d.user,
		Password: d.pass,
		// Governs reads and writes, not the dial — our DialFunc owns
		// that. Generous because it applies to each read of a data
		// transfer, and a slow link is not a broken one.
		Timeout: 30 * time.Second,
		DialFunc: func(network, address string) (net.Conn, error) {
			raw, err := dialer.Dial(network, address)
			if err != nil {
				return nil, err
			}
			// Plain, even for FTPS: the library performs the handshake
			// on top of what we return, which is exactly the wrapping
			// order that makes the counters read ciphertext.
			return api.WrapConn(raw, stats), nil
		},
	}
	if d.user == "" {
		cfg.User = "anonymous"
		cfg.Password = ""
	}
	if tlsCfg != nil {
		cfg.TLSConfig = tlsCfg
		cfg.TLSMode = goftp.TLSImplicit
	}

	c, err := goftp.DialConfig(cfg, addr)
	if err != nil {
		return err
	}

	// DialConfig does not connect — the client opens connections from a
	// pool on first use. Mounting has to fail here rather than at the
	// first directory listing, so this forces one connection and one
	// login now, and a bad host or a wrong password is reported by
	// Mount as it was before.
	if _, err := c.Getwd(); err != nil {
		c.Close()
		return err
	}

	d.client = c
	return nil
}

// Unmount closes FTP connection
func (d *FTPDriver) Unmount(mountID int) error {
	if d.client != nil {
		d.client.Close()
		d.client = nil
	}
	d.connected = false
	return nil
}

// isConnError checks if error is a connection error that warrants reconnect
func (d *FTPDriver) isConnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset")
}

// withReconnect executes an operation, reconnecting on connection errors
func (d *FTPDriver) withReconnect(op func() error) error {
	err := op()
	if d.isConnError(err) && d.client != nil {
		d.client.Close()
		d.client = nil
		if retryErr := d.connect(); retryErr != nil {
			return retryErr
		}
		return op()
	}
	return err
}

// Stat retrieves file/directory info.
//
// The FTP client handles the MLST/LIST distinction itself — servers that
// answer 502 to MLST are retried with LIST inside it — so this no longer
// carries its own fallback. That fallback, and the 502 detection that
// drove it, were removed rather than left as code nothing reaches.
func (d *FTPDriver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return api.FileInfo{}, api.ErrNotConnected
	}

	if path == "" || path == "/" {
		return api.FileInfo{Name: "/", Path: "/", IsDir: true}, nil
	}

	var info api.FileInfo
	absPath := d.rootPath + path

	err := d.withReconnect(func() error {
		fi, err := d.client.Stat(absPath)
		if err != nil {
			return err
		}
		info = fileInfoFrom(fi, path)
		return nil
	})

	return info, err
}

// fileInfoFrom converts what the FTP client reports into this package's
// own shape. `path` is the mount-relative path, which the client does
// not know: it answers about the absolute path it was asked for.
func fileInfoFrom(fi os.FileInfo, path string) api.FileInfo {
	return api.FileInfo{
		Name:    fi.Name(),
		Path:    path,
		IsDir:   fi.IsDir(),
		Size:    fi.Size(),
		ModTime: fi.ModTime().Unix(),
	}
}

// ListDir returns directory entries
func (d *FTPDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	absPath := d.rootPath + path
	var result []api.FileInfo

	err := d.withReconnect(func() error {
		entries, err := d.client.ReadDir(absPath)
		if err != nil {
			return err
		}

		var out []api.FileInfo
		for _, e := range entries {
			// Kept even though the client filters them: a server that
			// reports them anyway would otherwise put "." in a listing.
			if e.Name() == "." || e.Name() == ".." {
				continue
			}
			out = append(out, fileInfoFrom(e, path+"/"+e.Name()))
		}
		result = out
		return nil
	})

	return result, err
}

// OpenFile returns a reader for file contents
func (d *FTPDriver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	absPath := d.rootPath + path

	// The client writes a file to an io.Writer rather than handing back
	// a reader, so a pipe turns the push into the pull this interface
	// promises. The transfer runs in its own goroutine and closes the
	// pipe with whatever it ended with, so the reader sees a clean EOF
	// on success and the real error otherwise.
	//
	// This deliberately does not go through withReconnect. Retrying is
	// only safe for an operation that can be repeated from the start,
	// and by the time a streaming transfer fails the caller may already
	// have read part of it — a retry would hand them the beginning of
	// the file twice.
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(d.client.Retrieve(absPath, pw))
	}()

	// Block until the first byte arrives or the transfer fails, so that
	// opening something unreadable is reported here rather than at the
	// caller's first Read. Callers rely on that: a missing file has
	// always been an error from OpenFile.
	first := make([]byte, 1)
	n, err := pr.Read(first)
	if err != nil && err != io.EOF {
		pr.CloseWithError(err)
		return nil, err
	}

	return &primedReader{first: first[:n], rest: pr}, nil
}

// primedReader replays the byte OpenFile consumed to find out whether
// the transfer was going to work, then gets out of the way.
type primedReader struct {
	first []byte
	rest  *io.PipeReader
}

func (r *primedReader) Read(p []byte) (int, error) {
	if len(r.first) > 0 {
		n := copy(p, r.first)
		r.first = r.first[n:]
		return n, nil
	}
	return r.rest.Read(p)
}

// Close stops the transfer. The goroutine feeding the pipe unblocks on
// its next write, so abandoning a partly-read file does not leak it.
func (r *primedReader) Close() error {
	r.first = nil
	return r.rest.Close()
}

// CreateFile returns a writer that streams into an upload.
//
// It used to gather the whole file in memory and send it from Close,
// which made the cost of copying a file its size in RAM. The upload now
// runs concurrently and the writer feeds it, so a large file costs a
// pipe buffer.
type ftpWriter struct {
	pw   *io.PipeWriter
	done chan error
	once sync.Once
	err  error
}

func (w *ftpWriter) Write(p []byte) (int, error) {
	return w.pw.Write(p)
}

// Close ends the upload and waits for it to finish.
//
// The wait is the point: a write that the server rejects fails the
// upload goroutine, not the Write call that fed it, so without waiting
// here a failed upload would look like a successful one. Close is the
// last chance to say otherwise.
func (w *ftpWriter) Close() error {
	w.once.Do(func() {
		w.pw.Close()
		w.err = <-w.done
	})
	return w.err
}

func (d *FTPDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	absPath := d.rootPath + path
	pr, pw := io.Pipe()
	done := make(chan error, 1)

	// Not through withReconnect, for the same reason as OpenFile: the
	// pipe can only be read once, so a retry would upload whatever was
	// left of it rather than the file.
	go func() {
		err := d.client.Store(absPath, pr)
		// Unblock any Write still waiting on a pipe nothing is reading.
		pr.CloseWithError(err)
		done <- err
	}()

	return &ftpWriter{pw: pw, done: done}, nil
}

// Mkdir creates a directory
func (d *FTPDriver) Mkdir(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	absPath := d.rootPath + path
	return d.withReconnect(func() error {
		_, err := d.client.Mkdir(absPath)
		return err
	})
}

// Remove deletes a file or directory
func (d *FTPDriver) Remove(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	absPath := d.rootPath + path

	// Try to delete as file first
	err := d.withReconnect(func() error {
		return d.client.Delete(absPath)
	})
	if err == nil {
		return nil
	}

	// If that fails, try to remove as directory
	return d.withReconnect(func() error {
		return d.client.Rmdir(absPath)
	})
}

// Rename moves/renames a file
func (d *FTPDriver) Rename(mountID int, oldPath, newPath string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	absOldPath := d.rootPath + oldPath
	absNewPath := d.rootPath + newPath

	return d.withReconnect(func() error {
		return d.client.Rename(absOldPath, absNewPath)
	})
}

// Helper to extract filename from path
func (d *FTPDriver) nameFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return path
}
