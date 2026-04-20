// smb/smb.go - SMB/CIFS filesystem driver
//
// This package implements the api.Driver interface for SMB/CIFS
// network shares. It provides read/write access to remote SMB servers.
//
// Migrated from diskjockey-backend/disktypes/smb.go

package smb

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/christhomas/go-networkfs/pkg/api"
	"github.com/hirochachacha/go-smb2"
)

// Driver type ID - must match dispatcher registry
const DriverTypeID = 3

func init() {
	// Register this driver with the global registry
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &SMBDriver{}
	})
}

// SMBDriver implements the Driver interface for SMB/CIFS connections
type SMBDriver struct {
	connected bool
	host      string
	port      int
	user      string
	pass      string
	share     string
	rootPath  string
	conn      net.Conn
	session   *smb2.Session
	shareFS   *smb2.Share
}

// Name returns the driver identifier
func (d *SMBDriver) Name() string {
	return "smb"
}

// Mount establishes the SMB connection, session and mounts the share.
// Config expects: host, port, user, pass, share, root
func (d *SMBDriver) Mount(mountID int, config map[string]string) error {
	host, ok := config["host"]
	if !ok || host == "" {
		return &api.DriverError{Code: 10, Message: "config missing 'host'"}
	}

	shareName, ok := config["share"]
	if !ok || shareName == "" {
		return &api.DriverError{Code: 10, Message: "config missing 'share'"}
	}

	user := config["user"]
	pass := config["pass"]

	rootPath := config["root"]
	if rootPath == "" {
		rootPath = "/"
	}

	portStr := config["port"]
	if portStr == "" {
		portStr = "445"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return &api.DriverError{Code: 11, Message: "invalid port: " + portStr}
	}

	d.host = host
	d.port = port
	d.user = user
	d.pass = pass
	d.share = shareName
	d.rootPath = rootPath

	if err := d.connect(); err != nil {
		return &api.DriverError{Code: 12, Message: "connection failed: " + err.Error()}
	}

	d.connected = true
	return nil
}

func (d *SMBDriver) connect() error {
	addr := fmt.Sprintf("%s:%d", d.host, d.port)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to dial SMB: %w", err)
	}

	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     d.user,
			Password: d.pass,
		},
	}

	session, err := dialer.Dial(conn)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMB dial failed: %w", err)
	}

	share, err := session.Mount(d.share)
	if err != nil {
		session.Logoff()
		conn.Close()
		return fmt.Errorf("SMB mount failed: %w", err)
	}

	d.conn = conn
	d.session = session
	d.shareFS = share
	return nil
}

// Unmount tears down share then session then connection.
func (d *SMBDriver) Unmount(mountID int) error {
	if d.shareFS != nil {
		d.shareFS.Umount()
		d.shareFS = nil
	}
	if d.session != nil {
		d.session.Logoff()
		d.session = nil
	}
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
	d.connected = false
	return nil
}

// isConnError checks if err looks like a transient connection failure.
func (d *SMBDriver) isConnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "session expired")
}

// withReconnect runs op; on transient conn errors it tears down and reconnects once.
func (d *SMBDriver) withReconnect(op func() error) error {
	err := op()
	if d.isConnError(err) {
		if d.shareFS != nil {
			d.shareFS.Umount()
			d.shareFS = nil
		}
		if d.session != nil {
			d.session.Logoff()
			d.session = nil
		}
		if d.conn != nil {
			d.conn.Close()
			d.conn = nil
		}
		if retryErr := d.connect(); retryErr != nil {
			return retryErr
		}
		return op()
	}
	return err
}

// cleanPath strips the rootPath prefix semantics. SMB share paths are
// relative to the share root and must not have a leading '/'. Empty
// path maps to "." (the share root).
func (d *SMBDriver) cleanPath(path string) string {
	// Combine root and path, then normalize.
	combined := d.rootPath
	if !strings.HasSuffix(combined, "/") {
		combined += "/"
	}
	if strings.HasPrefix(path, "/") {
		combined += path[1:]
	} else {
		combined += path
	}

	// Strip leading slashes — SMB API wants relative paths.
	for strings.HasPrefix(combined, "/") {
		combined = combined[1:]
	}

	// Collapse empty to "."
	if combined == "" {
		return "."
	}
	return combined
}

// Stat returns file/directory metadata.
func (d *SMBDriver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected || d.shareFS == nil {
		return api.FileInfo{}, api.ErrNotConnected
	}

	var info api.FileInfo
	cp := d.cleanPath(path)

	err := d.withReconnect(func() error {
		fi, err := d.shareFS.Stat(cp)
		if err != nil {
			return err
		}
		info = api.FileInfo{
			Name:    fi.Name(),
			Path:    path,
			IsDir:   fi.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
			Mode:    uint32(fi.Mode()),
		}
		return nil
	})

	return info, err
}

// ListDir returns entries in directory at path.
func (d *SMBDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected || d.shareFS == nil {
		return nil, api.ErrNotConnected
	}

	cp := d.cleanPath(path)
	var result []api.FileInfo

	err := d.withReconnect(func() error {
		files, err := d.shareFS.ReadDir(cp)
		if err != nil {
			return err
		}

		var out []api.FileInfo
		for _, f := range files {
			name := f.Name()
			if name == "." || name == ".." {
				continue
			}
			childPath := path
			if !strings.HasSuffix(childPath, "/") {
				childPath += "/"
			}
			childPath += name
			out = append(out, api.FileInfo{
				Name:    name,
				Path:    childPath,
				IsDir:   f.IsDir(),
				Size:    f.Size(),
				ModTime: f.ModTime().Unix(),
				Mode:    uint32(f.Mode()),
			})
		}
		result = out
		return nil
	})

	return result, err
}

// OpenFile opens a file for reading.
func (d *SMBDriver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected || d.shareFS == nil {
		return nil, api.ErrNotConnected
	}

	cp := d.cleanPath(path)
	var reader io.ReadCloser

	err := d.withReconnect(func() error {
		f, err := d.shareFS.Open(cp)
		if err != nil {
			return err
		}
		reader = f
		return nil
	})

	return reader, err
}

// CreateFile opens/truncates a file for writing.
func (d *SMBDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected || d.shareFS == nil {
		return nil, api.ErrNotConnected
	}

	cp := d.cleanPath(path)
	if cp == "." {
		return nil, &api.DriverError{Code: 13, Message: "cannot write to root directory"}
	}

	var writer io.WriteCloser

	err := d.withReconnect(func() error {
		f, err := d.shareFS.Create(cp)
		if err != nil {
			return err
		}
		writer = f
		return nil
	})

	return writer, err
}

// Mkdir creates a directory with 0755 perms.
func (d *SMBDriver) Mkdir(mountID int, path string) error {
	if !d.connected || d.shareFS == nil {
		return api.ErrNotConnected
	}

	cp := d.cleanPath(path)
	return d.withReconnect(func() error {
		return d.shareFS.Mkdir(cp, 0755)
	})
}

// Remove deletes a file. If that fails (e.g. directory), tries RemoveAll.
func (d *SMBDriver) Remove(mountID int, path string) error {
	if !d.connected || d.shareFS == nil {
		return api.ErrNotConnected
	}

	cp := d.cleanPath(path)
	if cp == "." {
		return &api.DriverError{Code: 13, Message: "cannot remove root directory"}
	}

	// Try as file first.
	err := d.withReconnect(func() error {
		return d.shareFS.Remove(cp)
	})
	if err == nil {
		return nil
	}

	// Fall back to recursive directory removal.
	return d.withReconnect(func() error {
		return d.shareFS.RemoveAll(cp)
	})
}

// Rename moves or renames a file/directory.
func (d *SMBDriver) Rename(mountID int, oldPath, newPath string) error {
	if !d.connected || d.shareFS == nil {
		return api.ErrNotConnected
	}

	absOld := d.cleanPath(oldPath)
	absNew := d.cleanPath(newPath)

	return d.withReconnect(func() error {
		return d.shareFS.Rename(absOld, absNew)
	})
}
