// pkg/api/driver.go - Shared filesystem driver interface
//
// This package defines the common interface that all network filesystem
// drivers must implement. It provides the abstraction layer between
// protocol-specific implementations (FTP, SFTP, SMB, etc.) and the
// unified C API exported via cgo.

package api

import "io"

// FileInfo represents metadata about a file or directory
type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime int64  `json:"mod_time"`
	Mode    uint32 `json:"mode"`
}

// Driver is the interface all network filesystem backends implement
type Driver interface {
	// Name returns the driver type identifier (e.g., "ftp", "sftp", "smb")
	Name() string

	// Mount establishes a connection to the remote filesystem
	// config contains driver-specific connection parameters
	Mount(mountID int, config map[string]string) error

	// Unmount closes the connection and cleans up resources
	Unmount(mountID int) error

	// Stat retrieves file/directory metadata
	Stat(mountID int, path string) (FileInfo, error)

	// ListDir returns entries in a directory
	ListDir(mountID int, path string) ([]FileInfo, error)

	// OpenFile returns a ReadCloser for file contents
	OpenFile(mountID int, path string) (io.ReadCloser, error)

	// CreateFile creates/truncates a file for writing
	CreateFile(mountID int, path string) (io.WriteCloser, error)

	// Mkdir creates a directory
	Mkdir(mountID int, path string) error

	// Remove deletes a file or directory
	Remove(mountID int, path string) error

	// Rename moves/renames a file or directory
	Rename(mountID int, oldPath, newPath string) error
}

// DriverFactory creates new Driver instances
type DriverFactory func() Driver

// Registry maintains the mapping of driver types to implementations
var registry = make(map[int]DriverFactory)

// RegisterDriver registers a driver type with its factory
// driverType: 1=FTP, 2=SFTP, 3=SMB, 4=Dropbox, 5=WebDAV, 6=GDrive, 7=S3, etc.
func RegisterDriver(driverType int, factory DriverFactory) {
	registry[driverType] = factory
}

// GetDriver creates a new Driver instance for the given type
func GetDriver(driverType int) (Driver, bool) {
	factory, ok := registry[driverType]
	if !ok {
		return nil, false
	}
	return factory(), true
}

// ListDriverTypes returns all registered driver type IDs
func ListDriverTypes() []int {
	types := make([]int, 0, len(registry))
	for t := range registry {
		types = append(types, t)
	}
	return types
}

// MountManager tracks active driver instances by mountID
type MountManager struct {
	mounts map[int]Driver
}

func NewMountManager() *MountManager {
	return &MountManager{
		mounts: make(map[int]Driver),
	}
}

func (m *MountManager) Mount(mountID, driverType int, config map[string]string) error {
	driver, ok := GetDriver(driverType)
	if !ok {
		return ErrUnknownDriverType
	}
	if err := driver.Mount(mountID, config); err != nil {
		return err
	}
	m.mounts[mountID] = driver
	return nil
}

func (m *MountManager) Unmount(mountID int) error {
	driver, ok := m.mounts[mountID]
	if !ok {
		return ErrMountNotFound
	}
	delete(m.mounts, mountID)
	return driver.Unmount(mountID)
}

func (m *MountManager) Get(mountID int) (Driver, bool) {
	d, ok := m.mounts[mountID]
	return d, ok
}

// Common errors
var (
	ErrUnknownDriverType = &DriverError{Code: 1, Message: "unknown driver type"}
	ErrMountNotFound     = &DriverError{Code: 2, Message: "mount not found"}
	ErrNotConnected      = &DriverError{Code: 3, Message: "not connected"}
	ErrPermissionDenied  = &DriverError{Code: 4, Message: "permission denied"}
	ErrNotFound          = &DriverError{Code: 5, Message: "file not found"}
	ErrExists            = &DriverError{Code: 6, Message: "file exists"}
)

// DriverError represents filesystem operation errors
type DriverError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements the error interface.
//
// Uses a value receiver so both `DriverError{...}` and `&DriverError{...}`
// satisfy `error`. A pointer receiver silently broke callers that returned
// a value type — the struct literal compiled fine at the call site but
// produced a non-error value.
func (e DriverError) Error() string {
	return e.Message
}
