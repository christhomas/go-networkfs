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
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/christhomas/go-networkfs/pkg/api"
	"github.com/jlaffaye/ftp"
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
	client    *ftp.ServerConn
}

// Name returns the driver identifier
func (d *FTPDriver) Name() string {
	return "ftp"
}

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

	if err := d.connect(); err != nil {
		return classifyFTPConnectError(err, host, port, user)
	}

	if _, err := d.client.List(rootPath); err != nil {
		_ = d.client.Quit()
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

// isFTPAuthError recognises the FTP auth-failed status codes.
// 530 = "Not logged in", 532 = "Need account for storing files".
func isFTPAuthError(err error) bool {
	if te, ok := err.(*textproto.Error); ok {
		return te.Code == 530 || te.Code == 532
	}
	msg := err.Error()
	return strings.Contains(msg, "530 ") ||
		strings.Contains(strings.ToLower(msg), "login incorrect")
}

// classifyFTPPathError maps the error from listing the remote root
// directory onto "path doesn't exist" / "permission denied" / generic.
func classifyFTPPathError(err error, path string) error {
	if te, ok := err.(*textproto.Error); ok {
		switch te.Code {
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

	opts := []ftp.DialOption{
		ftp.DialWithTimeout(5 * time.Second),
	}

	if d.ftps {
		opts = append(opts, ftp.DialWithTLS(&tls.Config{InsecureSkipVerify: true}))
	}

	c, err := ftp.Dial(addr, opts...)
	if err != nil {
		return err
	}

	if d.user != "" {
		if err := c.Login(d.user, d.pass); err != nil {
			c.Quit()
			return err
		}
	} else {
		if err := c.Login("anonymous", ""); err != nil {
			c.Quit()
			return err
		}
	}

	d.client = c
	return nil
}

// Unmount closes FTP connection
func (d *FTPDriver) Unmount(mountID int) error {
	if d.client != nil {
		d.client.Quit()
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
		d.client.Quit()
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
// Primary path is MLST via GetEntry. Servers that don't implement MLST
// (e.g. vsftpd returns 502 Command not implemented) fall back to listing
// the parent directory and matching by name.
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
		e, err := d.client.GetEntry(absPath)
		if err == nil {
			info = entryToFileInfo(e, path)
			return nil
		}
		if isNotImplemented(err) {
			return d.statViaParentList(path, &info)
		}
		return err
	})

	return info, err
}

// statViaParentList resolves Stat by LIST-ing the parent directory and
// matching the basename. Used when the server refuses MLST.
func (d *FTPDriver) statViaParentList(path string, out *api.FileInfo) error {
	absPath := d.rootPath + path
	parentAbs, name := splitParentName(absPath)
	entries, err := d.client.List(parentAbs)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name == name {
			*out = entryToFileInfo(e, path)
			return nil
		}
	}
	return api.ErrNotFound
}

func entryToFileInfo(e *ftp.Entry, path string) api.FileInfo {
	return api.FileInfo{
		Name:    e.Name,
		Path:    path,
		IsDir:   e.Type == ftp.EntryTypeFolder,
		Size:    int64(e.Size),
		ModTime: time.Time(e.Time).Unix(),
	}
}

func isNotImplemented(err error) bool {
	if err == nil {
		return false
	}
	if te, ok := err.(*textproto.Error); ok {
		return te.Code == ftp.StatusNotImplemented ||
			te.Code == ftp.StatusNotImplementedParameter
	}
	return strings.Contains(err.Error(), "502")
}

func splitParentName(absPath string) (parent, name string) {
	trimmed := strings.TrimRight(absPath, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return "/", trimmed
	}
	if idx == 0 {
		return "/", trimmed[1:]
	}
	return trimmed[:idx], trimmed[idx+1:]
}

// ListDir returns directory entries
func (d *FTPDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	absPath := d.rootPath + path
	var result []api.FileInfo

	err := d.withReconnect(func() error {
		entries, err := d.client.List(absPath)
		if err != nil {
			return err
		}

		var out []api.FileInfo
		for _, e := range entries {
			if e.Name == "." || e.Name == ".." {
				continue
			}
			out = append(out, api.FileInfo{
				Name:    e.Name,
				Path:    path + "/" + e.Name,
				IsDir:   e.Type == ftp.EntryTypeFolder,
				Size:    int64(e.Size),
				ModTime: time.Time(e.Time).Unix(),
			})
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
	var reader io.ReadCloser

	err := d.withReconnect(func() error {
		r, err := d.client.Retr(absPath)
		if err != nil {
			return err
		}
		reader = r
		return nil
	})

	return reader, err
}

// CreateFile returns a writer for file creation
// Note: FTP STOR requires the data to be available at call time,
// so we collect data and store in Close()
type ftpWriter struct {
	data   []byte
	driver *FTPDriver
	path   string
	closed bool
}

func (w *ftpWriter) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, fmt.Errorf("writer closed")
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *ftpWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.driver.withReconnect(func() error {
		absPath := w.driver.rootPath + w.path
		return w.driver.client.Stor(absPath, strings.NewReader(string(w.data)))
	})
}

func (d *FTPDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	return &ftpWriter{
		driver: d,
		path:   path,
		data:   make([]byte, 0),
	}, nil
}

// Mkdir creates a directory
func (d *FTPDriver) Mkdir(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	absPath := d.rootPath + path
	return d.withReconnect(func() error {
		return d.client.MakeDir(absPath)
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
		return d.client.RemoveDir(absPath)
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
