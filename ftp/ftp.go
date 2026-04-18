// ftp/ftp.go - FTP filesystem driver
//
// This package implements the api.Driver interface for FTP/FTPS
// connections. It provides read/write access to remote FTP servers.

package ftp

import (
	"fmt"
	"io"
	"strconv"
	"time"

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
	user      string
	pass      string
	// TODO: Add actual FTP client connection
}

// Name returns the driver identifier
func (d *FTPDriver) Name() string {
	return "ftp"
}

// Mount establishes FTP connection
func (d *FTPDriver) Mount(mountID int, config map[string]string) error {
	host, ok := config["host"]
	if !ok {
		return fmt.Errorf("config missing 'host'")
	}
	user := config["user"]
	pass := config["pass"]
	portStr := config["port"]
	if portStr == "" {
		portStr = "21"
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port: %s", portStr)
	}

	d.host = host
	d.user = user
	d.pass = pass

	// TODO: Actually connect using ftp library
	fmt.Printf("[FTP] Connecting to %s:%d as %s\n", host, port, user)
	d.connected = true
	return nil
}

// Unmount closes FTP connection
func (d *FTPDriver) Unmount(mountID int) error {
	d.connected = false
	fmt.Printf("[FTP] Disconnected from %s\n", d.host)
	return nil
}

// Stat retrieves file/directory info
func (d *FTPDriver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected {
		return api.FileInfo{}, api.ErrNotConnected
	}

	// TODO: Implement actual FTP STAT
	return api.FileInfo{
		Name:    "placeholder",
		Path:    path,
		Size:    0,
		IsDir:   false,
		ModTime: time.Now().Unix(),
		Mode:    0644,
	}, nil
}

// ListDir returns directory entries
func (d *FTPDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}

	// TODO: Implement actual FTP LIST
	return []api.FileInfo{}, nil
}

// OpenFile returns a reader for file contents
func (d *FTPDriver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}

	// TODO: Implement actual FTP RETR
	return nil, fmt.Errorf("not implemented")
}

// CreateFile returns a writer for file creation
func (d *FTPDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}

	// TODO: Implement actual FTP STOR
	return nil, fmt.Errorf("not implemented")
}

// Mkdir creates a directory
func (d *FTPDriver) Mkdir(mountID int, path string) error {
	if !d.connected {
		return api.ErrNotConnected
	}

	// TODO: Implement actual FTP MKD
	return fmt.Errorf("not implemented")
}

// Remove deletes a file or directory
func (d *FTPDriver) Remove(mountID int, path string) error {
	if !d.connected {
		return api.ErrNotConnected
	}

	// TODO: Implement actual FTP DELE/RMD
	return fmt.Errorf("not implemented")
}

// Rename moves/renames a file
func (d *FTPDriver) Rename(mountID int, oldPath, newPath string) error {
	if !d.connected {
		return api.ErrNotConnected
	}

	// TODO: Implement actual FTP RNFR/RNTO
	return fmt.Errorf("not implemented")
}
