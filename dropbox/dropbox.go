// dropbox/dropbox.go - Dropbox filesystem driver
//
// This package implements the api.Driver interface for Dropbox cloud
// storage via the Dropbox HTTP API.
//
// Migrated from diskjockey-backend/disktypes/dropbox.go

package dropbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/christhomas/go-networkfs/pkg/api"
	dbx "github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
	"golang.org/x/oauth2"
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
	connected bool
	client    files.Client
	// Per-mount transport counters fed by api.CountingTransport. The
	// host-side IOStatsCollector polls a Snapshot() each tick.
	stats *api.MountStats
}

// Name returns the driver identifier
func (d *DropboxDriver) Name() string {
	return "dropbox"
}

// Stats implements api.StatsProvider so the MountManager can hand
// our transport counters back through the C ABI.
func (d *DropboxDriver) Stats() *api.MountStats { return d.stats }

// Mount sets up the Dropbox API client.
//
// Config shapes (in priority order):
//
//  1. {app_key, refresh_token} — current PKCE flow. We wrap an
//     oauth2.Config with the app_key and Dropbox's token endpoint;
//     the SDK gets an *http.Client that auto-refreshes the access
//     token on expiry. No persisted access_token.
//
//  2. {access_token} — legacy long-lived token. Still accepted so
//     existing mounts keep working until the user re-authenticates;
//     remove once we've shipped the OAuth flow for a release.
//
// Mount fails if neither shape is present.
func (d *DropboxDriver) Mount(mountID int, config map[string]string) error {
	appKey := config["app_key"]
	refreshToken := config["refresh_token"]
	legacyToken := config["access_token"]

	d.stats = &api.MountStats{}

	if appKey != "" && refreshToken != "" {
		// Modern PKCE flow. The oauth2.Token starts with no
		// AccessToken — the TokenSource immediately refreshes on
		// the first request because Expiry is zero (treated as
		// already-expired). Subsequent calls reuse the cached
		// access token until it ages out.
		oauthConf := &oauth2.Config{
			ClientID: appKey,
			Endpoint: oauth2.Endpoint{
				TokenURL: "https://api.dropbox.com/oauth2/token",
			},
		}
		tok := &oauth2.Token{
			RefreshToken: refreshToken,
			// AccessToken left empty + Expiry zero ⇒ first call
			// triggers a refresh.
		}
		// Wrap the OAuth-refreshing client so our CountingTransport
		// sees every request (including the silent token-refresh POSTs
		// the oauth2 transport issues).
		httpClient := api.WrapHTTPClient(oauthConf.Client(context.Background(), tok), d.stats)
		cfg := dbx.Config{
			Client:   httpClient,
			LogLevel: dbx.LogInfo,
		}
		d.client = files.New(cfg)
		d.connected = true
		return nil
	}

	if legacyToken != "" {
		// Provide our own HTTP client so the SDK's transport flows
		// through the byte counter. The SDK still owns auth (it adds
		// the Bearer header from cfg.Token).
		cfg := dbx.Config{
			Token:    legacyToken,
			Client:   api.WrapHTTPClient(nil, d.stats),
			LogLevel: dbx.LogInfo,
		}
		d.client = files.New(cfg)
		d.connected = true
		return nil
	}

	return &api.DriverError{
		Code:    10,
		Message: "config missing credentials: provide {app_key, refresh_token} (preferred) or legacy {access_token}",
	}
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

// thumbnailBucketTag returns the Dropbox ThumbnailSize tag value for
// the smallest provider bucket >= sizePx (long edge). Falls back to
// the largest bucket for unreasonably large requests.
func thumbnailBucketTag(sizePx int) string {
	switch {
	case sizePx <= 32:
		return files.ThumbnailSizeW32h32
	case sizePx <= 64:
		return files.ThumbnailSizeW64h64
	case sizePx <= 128:
		return files.ThumbnailSizeW128h128
	case sizePx <= 256:
		return files.ThumbnailSizeW256h256
	case sizePx <= 480:
		return files.ThumbnailSizeW480h320
	case sizePx <= 640:
		return files.ThumbnailSizeW640h480
	case sizePx <= 960:
		return files.ThumbnailSizeW960h640
	case sizePx <= 1024:
		return files.ThumbnailSizeW1024h768
	default:
		return files.ThumbnailSizeW2048h1536
	}
}

// GetThumbnail implements api.Thumbnailer for Dropbox using the
// files/get_thumbnail_v2 endpoint. Returns JPEG bytes.
func (d *DropboxDriver) GetThumbnail(mountID int, path string, sizePx int) ([]byte, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	resource := &files.PathOrLink{
		Tagged: dbx.Tagged{Tag: files.PathOrLinkPath},
		Path:   dbxPath(path),
	}
	arg := files.NewThumbnailV2Arg(resource)
	arg.Format = &files.ThumbnailFormat{Tagged: dbx.Tagged{Tag: files.ThumbnailFormatJpeg}}
	arg.Size = &files.ThumbnailSize{Tagged: dbx.Tagged{Tag: thumbnailBucketTag(sizePx)}}

	_, content, err := d.client.GetThumbnailV2(arg)
	if err != nil {
		return nil, wrapDbxError(err)
	}
	defer content.Close()

	data, err := io.ReadAll(content)
	if err != nil {
		return nil, wrapDbxError(err)
	}
	return data, nil
}
