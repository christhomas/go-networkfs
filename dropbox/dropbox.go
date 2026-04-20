// dropbox/dropbox.go - Dropbox filesystem driver
//
// This package implements the api.Driver interface for Dropbox cloud
// storage via the Dropbox HTTP API.
//
// Migrated from diskjockey-backend/disktypes/dropbox.go

package dropbox

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/christhomas/go-networkfs/pkg/api"
	dbx "github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
)

// Driver type ID - must match dispatcher registry
const DriverTypeID = 4

func init() {
	// Register this driver with the global registry
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &DropboxDriver{}
	})
}

// DropboxDriver implements the Driver interface for Dropbox connections
type DropboxDriver struct {
	connected   bool
	accessToken string
	client      files.Client
}

// Name returns the driver identifier
func (d *DropboxDriver) Name() string {
	return "dropbox"
}

// Mount sets up the Dropbox API client
// Config expects: access_token
func (d *DropboxDriver) Mount(mountID int, config map[string]string) error {
	token, ok := config["access_token"]
	if !ok || token == "" {
		return &api.DriverError{Code: 10, Message: "config missing 'access_token'"}
	}

	d.accessToken = token

	cfg := dbx.Config{
		Token:    token,
		LogLevel: dbx.LogInfo,
	}
	d.client = files.New(cfg)
	d.connected = true
	return nil
}

// Unmount is a no-op for Dropbox (HTTP-based, no persistent connection)
func (d *DropboxDriver) Unmount(mountID int) error {
	d.client = nil
	d.connected = false
	return nil
}

// dbxPath normalizes a path for the Dropbox API.
// The root is represented as "" (empty string), not "/".
// All other paths must start with "/".
func dbxPath(path string) string {
	if path == "" || path == "/" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// wrapDbxError adds a helpful hint when the Dropbox API reports a
// missing_scope error.
func wrapDbxError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "missing_scope") {
		return fmt.Errorf("Dropbox API error: missing required permission scope. Please check your app's permissions and access token. (error: %s)", msg)
	}
	return err
}

// nameFromPath extracts the trailing component of a path.
func nameFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// Stat retrieves file/directory info
func (d *DropboxDriver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return api.FileInfo{}, api.ErrNotConnected
	}

	p := dbxPath(path)

	// Dropbox: the root cannot be queried via GetMetadata; synthesize it.
	if p == "" {
		return api.FileInfo{
			Name:  "",
			Path:  "/",
			IsDir: true,
			Size:  0,
		}, nil
	}

	arg := files.NewGetMetadataArg(p)
	meta, err := d.client.GetMetadata(arg)
	if err != nil {
		return api.FileInfo{}, wrapDbxError(err)
	}

	switch m := meta.(type) {
	case *files.FileMetadata:
		return api.FileInfo{
			Name:    m.Name,
			Path:    path,
			IsDir:   false,
			Size:    int64(m.Size),
			ModTime: m.ServerModified.Unix(),
		}, nil
	case *files.FolderMetadata:
		return api.FileInfo{
			Name:  m.Name,
			Path:  path,
			IsDir: true,
			Size:  0,
		}, nil
	default:
		return api.FileInfo{
			Name:  nameFromPath(path),
			Path:  path,
			IsDir: false,
		}, nil
	}
}

// ListDir returns directory entries
func (d *DropboxDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	p := dbxPath(path)
	arg := files.NewListFolderArg(p)
	res, err := d.client.ListFolder(arg)
	if err != nil {
		return nil, wrapDbxError(err)
	}

	var out []api.FileInfo
	for _, entry := range res.Entries {
		switch f := entry.(type) {
		case *files.FileMetadata:
			childPath := strings.TrimRight(path, "/") + "/" + f.Name
			out = append(out, api.FileInfo{
				Name:    f.Name,
				Path:    childPath,
				IsDir:   false,
				Size:    int64(f.Size),
				ModTime: f.ServerModified.Unix(),
			})
		case *files.FolderMetadata:
			childPath := strings.TrimRight(path, "/") + "/" + f.Name
			out = append(out, api.FileInfo{
				Name:  f.Name,
				Path:  childPath,
				IsDir: true,
				Size:  0,
			})
		}
	}
	return out, nil
}

// OpenFile returns a reader for file contents
func (d *DropboxDriver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	arg := files.NewDownloadArg(dbxPath(path))
	_, content, err := d.client.Download(arg)
	if err != nil {
		return nil, wrapDbxError(err)
	}
	return content, nil
}

// CreateFile returns a writer for file creation.
// Dropbox's Upload requires the reader at call time, so we buffer Write()
// calls and flush via client.Upload in Close().
type dropboxWriter struct {
	buf    bytes.Buffer
	driver *DropboxDriver
	path   string
	closed bool
}

func (w *dropboxWriter) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, fmt.Errorf("writer closed")
	}
	return w.buf.Write(p)
}

func (w *dropboxWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true

	arg := files.NewUploadArg(dbxPath(w.path))
	arg.Mode.Tag = "overwrite"
	_, err := w.driver.client.Upload(arg, &w.buf)
	return wrapDbxError(err)
}

func (d *DropboxDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	return &dropboxWriter{
		driver: d,
		path:   path,
	}, nil
}

// Mkdir creates a directory
func (d *DropboxDriver) Mkdir(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	arg := files.NewCreateFolderArg(dbxPath(path))
	_, err := d.client.CreateFolderV2(arg)
	return wrapDbxError(err)
}

// Remove deletes a file or directory (Dropbox's DeleteV2 handles both)
func (d *DropboxDriver) Remove(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	arg := files.NewDeleteArg(dbxPath(path))
	_, err := d.client.DeleteV2(arg)
	return wrapDbxError(err)
}

// Rename moves/renames a file or directory
func (d *DropboxDriver) Rename(mountID int, oldPath, newPath string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	arg := files.NewRelocationArg(dbxPath(oldPath), dbxPath(newPath))
	_, err := d.client.MoveV2(arg)
	return wrapDbxError(err)
}
