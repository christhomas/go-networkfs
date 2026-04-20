// webdav/webdav.go - WebDAV filesystem driver
//
// This package implements the api.Driver interface for WebDAV
// connections. It provides read/write access to remote WebDAV servers
// using the github.com/studio-b12/gowebdav client library.
//
// Migrated from diskjockey-backend/disktypes/webdav.go
//
// Defensive panic-recover blocks are intentionally preserved on every
// interface method because the upstream gowebdav library has a history
// of panicking on malformed responses.

package webdav

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/christhomas/go-networkfs/pkg/api"
	"github.com/studio-b12/gowebdav"
)

// Driver type ID - must match dispatcher registry
const DriverTypeID = 5

func init() {
	// Register this driver with the global registry
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &WebDAVDriver{}
	})
}

// WebDAVDriver implements the Driver interface for WebDAV connections
type WebDAVDriver struct {
	connected  bool
	baseURL    string
	user       string
	pass       string
	pathPrefix string
	client     *gowebdav.Client
}

// Name returns the driver identifier
func (d *WebDAVDriver) Name() string {
	return "webdav"
}

// Mount establishes a WebDAV client.
//
// Config keys (all strings):
//
//	url   - full base URL (e.g. https://webdav.example.com). Takes precedence.
//	host  - host name (used only if url is empty)
//	port  - port number as string (used only if url is empty)
//	user  - username
//	pass  - password
//	path  - URL-space prefix prepended to every request (e.g. /username)
//
// gowebdav.NewClient does not actually dial, so Mount is essentially
// validate-only; real connection errors surface on the first request.
func (d *WebDAVDriver) Mount(mountID int, config map[string]string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][Mount] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 12, Message: fmt.Sprintf("panic in mount: %v", r)}
		}
	}()

	user := config["user"]
	pass := config["pass"]
	pathPrefix := config["path"]

	var baseURL string
	if u := config["url"]; u != "" {
		baseURL = u
	} else {
		host := config["host"]
		if host == "" {
			return &api.DriverError{Code: 10, Message: "webdav: missing required config 'host' (or 'url')"}
		}

		scheme := "https"
		portStr := ""
		if p := config["port"]; p != "" {
			port, convErr := strconv.Atoi(p)
			if convErr != nil {
				return &api.DriverError{Code: 11, Message: "invalid port: " + p}
			}
			if port == 80 || port == 8080 {
				scheme = "http"
			}
			if port != 0 {
				portStr = fmt.Sprintf(":%d", port)
			}
		}

		baseURL = fmt.Sprintf("%s://%s%s", scheme, host, portStr)
	}

	d.baseURL = baseURL
	d.user = user
	d.pass = pass
	d.pathPrefix = pathPrefix

	d.client = gowebdav.NewClient(baseURL, user, pass)

	// Optional: validate with Connect (may no-op depending on server).
	if connErr := d.client.Connect(); connErr != nil {
		return &api.DriverError{Code: 12, Message: "webdav connect failed: " + connErr.Error()}
	}

	d.connected = true
	return nil
}

// Unmount is a no-op; gowebdav holds no persistent connection.
func (d *WebDAVDriver) Unmount(mountID int) error {
	d.client = nil
	d.connected = false
	return nil
}

// fullPath prepends pathPrefix to the requested path, ensuring exactly
// one slash between them. Mirrors the source driver's logic.
func (d *WebDAVDriver) fullPath(requested string) string {
	if d.pathPrefix == "" {
		return requested
	}

	cleanPath := d.pathPrefix
	if !strings.HasPrefix(cleanPath, "/") {
		cleanPath = "/" + cleanPath
	}
	if strings.HasSuffix(cleanPath, "/") {
		cleanPath = strings.TrimRight(cleanPath, "/")
	}

	req := requested
	if !strings.HasPrefix(req, "/") {
		req = "/" + req
	}

	return cleanPath + req
}

// nameFromPath extracts the last non-empty segment of a path.
func (d *WebDAVDriver) nameFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return path
}

// Stat retrieves file/directory metadata.
func (d *WebDAVDriver) Stat(mountID int, path string) (info api.FileInfo, err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][Stat] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 20, Message: fmt.Sprintf("panic in Stat: %v", r)}
			info = api.FileInfo{}
		}
	}()

	if !d.connected || d.client == nil {
		return api.FileInfo{}, api.ErrNotConnected
	}

	fi, statErr := d.client.Stat(d.fullPath(path))
	if statErr != nil {
		return api.FileInfo{}, statErr
	}

	name := fi.Name()
	if name == "" {
		name = d.nameFromPath(path)
	}

	return api.FileInfo{
		Name:    name,
		Path:    path,
		Size:    fi.Size(),
		IsDir:   fi.IsDir(),
		ModTime: fi.ModTime().Unix(),
		Mode:    uint32(fi.Mode()),
	}, nil
}

// ListDir returns directory entries.
func (d *WebDAVDriver) ListDir(mountID int, path string) (infos []api.FileInfo, err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][ListDir] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 21, Message: fmt.Sprintf("panic in ListDir: %v", r)}
			infos = nil
		}
	}()

	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	files, listErr := d.client.ReadDir(d.fullPath(path))
	if listErr != nil {
		return nil, listErr
	}

	trimmed := strings.TrimRight(path, "/")
	out := make([]api.FileInfo, 0, len(files))
	for _, f := range files {
		childPath := trimmed + "/" + f.Name()
		out = append(out, api.FileInfo{
			Name:    f.Name(),
			Path:    childPath,
			Size:    f.Size(),
			IsDir:   f.IsDir(),
			ModTime: f.ModTime().Unix(),
			Mode:    uint32(f.Mode()),
		})
	}

	return out, nil
}

// OpenFile returns a reader for file contents.
func (d *WebDAVDriver) OpenFile(mountID int, path string) (rc io.ReadCloser, err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][OpenFile] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 22, Message: fmt.Sprintf("panic in OpenFile: %v", r)}
			rc = nil
		}
	}()

	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	stream, readErr := d.client.ReadStream(d.fullPath(path))
	if readErr != nil {
		return nil, readErr
	}
	return stream, nil
}

// CreateFile returns a buffered writer that uploads on Close().
// gowebdav's Write takes a full byte slice, so we buffer in memory —
// same pattern used by the FTP driver's ftpWriter.
type webdavWriter struct {
	buf    bytes.Buffer
	driver *WebDAVDriver
	path   string
	closed bool
}

func (w *webdavWriter) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, fmt.Errorf("writer closed")
	}
	return w.buf.Write(p)
}

func (w *webdavWriter) Close() (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][Write] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 23, Message: fmt.Sprintf("panic in Close/Write: %v", r)}
		}
	}()

	if w.closed {
		return nil
	}
	w.closed = true

	if w.driver == nil || w.driver.client == nil {
		return api.ErrNotConnected
	}

	return w.driver.client.Write(w.driver.fullPath(w.path), w.buf.Bytes(), 0644)
}

func (d *WebDAVDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}
	return &webdavWriter{
		driver: d,
		path:   path,
	}, nil
}

// Mkdir creates a single directory.
func (d *WebDAVDriver) Mkdir(mountID int, path string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][Mkdir] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 24, Message: fmt.Sprintf("panic in Mkdir: %v", r)}
		}
	}()

	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}
	return d.client.Mkdir(d.fullPath(path), 0755)
}

// Remove deletes a file or directory. gowebdav's Remove handles both,
// but fall through to RemoveAll if it fails (e.g. non-empty directory).
func (d *WebDAVDriver) Remove(mountID int, path string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][Remove] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 25, Message: fmt.Sprintf("panic in Remove: %v", r)}
		}
	}()

	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	full := d.fullPath(path)
	if rmErr := d.client.Remove(full); rmErr == nil {
		return nil
	}
	return d.client.RemoveAll(full)
}

// Rename moves/renames a file or directory (overwrite=true).
func (d *WebDAVDriver) Rename(mountID int, oldPath, newPath string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[WebDAV][PANIC][Rename] %v\n%s\n", r, debug.Stack())
			err = &api.DriverError{Code: 26, Message: fmt.Sprintf("panic in Rename: %v", r)}
		}
	}()

	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}
	return d.client.Rename(d.fullPath(oldPath), d.fullPath(newPath), true)
}
