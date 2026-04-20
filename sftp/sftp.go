// sftp/sftp.go - SFTP filesystem driver
//
// This package implements the api.Driver interface for SFTP
// connections over SSH. It provides read/write access to remote
// SFTP servers.
//
// Migrated from diskjockey-backend/disktypes/sftp.go

package sftp

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/christhomas/go-networkfs/pkg/api"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Driver type ID - must match dispatcher registry
const DriverTypeID = 2

func init() {
	// Register this driver with the global registry
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &SFTPDriver{}
	})
}

// SFTPDriver implements the Driver interface for SFTP connections
type SFTPDriver struct {
	connected   bool
	host        string
	port        int
	user        string
	pass        string
	rootPath    string
	useSSHAgent bool
	sshConn     *ssh.Client
	client      *sftp.Client
}

// Name returns the driver identifier
func (d *SFTPDriver) Name() string {
	return "sftp"
}

// Mount establishes SFTP connection
// Config expects: host, port, user, pass, root, use_ssh_agent
func (d *SFTPDriver) Mount(mountID int, config map[string]string) error {
	host, ok := config["host"]
	if !ok || host == "" {
		return &api.DriverError{Code: 10, Message: "config missing 'host'"}
	}

	user := config["user"]
	pass := config["pass"]
	rootPath := config["root"]
	if rootPath == "" {
		rootPath = "/"
	}

	portStr := config["port"]
	if portStr == "" {
		portStr = "22"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return &api.DriverError{Code: 11, Message: "invalid port: " + portStr}
	}

	useAgent := config["use_ssh_agent"] == "true" || config["use_ssh_agent"] == "1"

	d.host = host
	d.port = port
	d.user = user
	d.pass = pass
	d.rootPath = rootPath
	d.useSSHAgent = useAgent

	// Establish connection
	if err := d.connect(); err != nil {
		return &api.DriverError{Code: 12, Message: "connection failed: " + err.Error()}
	}

	d.connected = true
	return nil
}

func (d *SFTPDriver) connect() error {
	if d.user == "" {
		return fmt.Errorf("missing required sftp user")
	}

	addr := fmt.Sprintf("%s:%d", d.host, d.port)

	auths := []ssh.AuthMethod{}
	if d.pass != "" {
		auths = append(auths, ssh.Password(d.pass))
	}

	if d.useSSHAgent {
		sshAgentSock := os.Getenv("SSH_AUTH_SOCK")
		if sshAgentSock != "" {
			agentConn, err := net.Dial("unix", sshAgentSock)
			if err == nil {
				auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers))
			}
		}
	}

	if len(auths) == 0 {
		return fmt.Errorf("no authentication method provided (set pass or use_ssh_agent)")
	}

	sshConfig := &ssh.ClientConfig{
		User:            d.user,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // WARNING: for demo only
		Timeout:         5 * time.Second,
	}

	sshConn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return fmt.Errorf("ssh dial failed: %w", err)
	}

	sftpClient, err := sftp.NewClient(sshConn)
	if err != nil {
		sshConn.Close()
		return fmt.Errorf("sftp client failed: %w", err)
	}

	d.sshConn = sshConn
	d.client = sftpClient
	return nil
}

// Unmount closes SFTP connection
func (d *SFTPDriver) Unmount(mountID int) error {
	var firstErr error
	if d.client != nil {
		if err := d.client.Close(); err != nil {
			firstErr = err
		}
		d.client = nil
	}
	if d.sshConn != nil {
		if err := d.sshConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		d.sshConn = nil
	}
	d.connected = false
	return firstErr
}

// isConnError checks if error is a connection error that warrants reconnect
func (d *SFTPDriver) isConnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection lost")
}

// withReconnect executes an operation, reconnecting on connection errors
func (d *SFTPDriver) withReconnect(op func() error) error {
	err := op()
	if d.isConnError(err) {
		if d.client != nil {
			d.client.Close()
			d.client = nil
		}
		if d.sshConn != nil {
			d.sshConn.Close()
			d.sshConn = nil
		}
		if retryErr := d.connect(); retryErr != nil {
			return retryErr
		}
		return op()
	}
	return err
}

// Stat retrieves file/directory info
func (d *SFTPDriver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return api.FileInfo{}, api.ErrNotConnected
	}

	var info api.FileInfo
	absPath := d.rootPath + path

	err := d.withReconnect(func() error {
		fi, err := d.client.Stat(absPath)
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

// ListDir returns directory entries
func (d *SFTPDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	absPath := d.rootPath + path
	var result []api.FileInfo

	err := d.withReconnect(func() error {
		files, err := d.client.ReadDir(absPath)
		if err != nil {
			return err
		}

		var out []api.FileInfo
		for _, f := range files {
			if f.Name() == "." || f.Name() == ".." {
				continue
			}
			out = append(out, api.FileInfo{
				Name:    f.Name(),
				Path:    path + "/" + f.Name(),
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

// OpenFile returns a reader for file contents
func (d *SFTPDriver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	absPath := d.rootPath + path
	var reader io.ReadCloser

	err := d.withReconnect(func() error {
		f, err := d.client.Open(absPath)
		if err != nil {
			return err
		}
		reader = f
		return nil
	})

	return reader, err
}

// CreateFile returns a writer for file creation
func (d *SFTPDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	absPath := d.rootPath + path
	var writer io.WriteCloser

	err := d.withReconnect(func() error {
		f, err := d.client.Create(absPath)
		if err != nil {
			return err
		}
		writer = f
		return nil
	})

	return writer, err
}

// Mkdir creates a directory
func (d *SFTPDriver) Mkdir(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	absPath := d.rootPath + path
	return d.withReconnect(func() error {
		return d.client.Mkdir(absPath)
	})
}

// Remove deletes a file or directory
func (d *SFTPDriver) Remove(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	absPath := d.rootPath + path

	// Try to delete as file first
	err := d.withReconnect(func() error {
		return d.client.Remove(absPath)
	})
	if err == nil {
		return nil
	}

	// If that fails, try to remove as directory
	return d.withReconnect(func() error {
		return d.client.RemoveDirectory(absPath)
	})
}

// Rename moves/renames a file
func (d *SFTPDriver) Rename(mountID int, oldPath, newPath string) error {
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
func (d *SFTPDriver) nameFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return path
}
