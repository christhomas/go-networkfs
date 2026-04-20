// onedrive/onedrive.go - OneDrive filesystem driver (Microsoft Graph v1.0)
//
// Authentication: OAuth2 refresh-token flow against the Microsoft identity
// platform. Callers supply client_id, client_secret, and a refresh_token
// obtained out-of-band. Access tokens are refreshed proactively based on
// the expires_in field and reactively on HTTP 401.
//
// Addressing: Graph supports native path addressing, so there is no
// path -> item-ID cache. All endpoints are built from the user-facing
// path directly.
//
// Uploads: bounded memory. Small writes stay in a 4 MiB buffer and ship
// via a single PUT. Writes that exceed the cap spill to a temp file, then
// stream back up through a Graph upload session in 10 MiB chunks, which
// is a valid multiple of the required 320 KiB alignment.
//
// Downloads: stream directly from resp.Body — the caller owns the close.

package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/christhomas/go-networkfs/pkg/api"
)

const DriverTypeID = 8

func init() {
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &OneDriveDriver{}
	})
}

const (
	graphBase    = "https://graph.microsoft.com/v1.0"
	tokenURL     = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	uploadMemCap = 4 * 1024 * 1024  // fits Graph's "small upload" ceiling
	chunkSize    = 10 * 1024 * 1024 // multiple of 320 KiB, well under 60 MiB cap
	maxRetries   = 3
)

type OneDriveDriver struct {
	clientID     string
	clientSecret string
	refreshToken string

	tokenMu     sync.Mutex
	accessToken string
	expiresAt   time.Time

	httpClient *http.Client
	connected  bool
}

func (d *OneDriveDriver) Name() string { return "onedrive" }

// Mount validates config and obtains an initial access token.
//
// Config keys:
//
//	client_id      - Azure app registration client ID (required)
//	client_secret  - client secret; empty for PKCE public clients (optional)
//	refresh_token  - long-lived refresh token from offline_access scope (required)
func (d *OneDriveDriver) Mount(mountID int, config map[string]string) error {
	d.clientID = config["client_id"]
	d.clientSecret = config["client_secret"]
	d.refreshToken = config["refresh_token"]
	if d.clientID == "" || d.refreshToken == "" {
		return &api.DriverError{Code: 10, Message: "onedrive: client_id and refresh_token are required"}
	}

	d.httpClient = &http.Client{Timeout: 60 * time.Second}
	if err := d.refresh(context.Background()); err != nil {
		return &api.DriverError{Code: 12, Message: "onedrive: initial token refresh failed: " + err.Error()}
	}
	d.connected = true
	return nil
}

func (d *OneDriveDriver) Unmount(mountID int) error {
	d.tokenMu.Lock()
	d.accessToken = ""
	d.tokenMu.Unlock()
	d.connected = false
	return nil
}

func (d *OneDriveDriver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected {
		return api.FileInfo{}, api.ErrNotConnected
	}
	p := normPath(path)
	resp, err := d.do(context.Background(), "GET", d.itemURL(p, ""), nil, nil)
	if err != nil {
		return api.FileInfo{}, err
	}
	defer resp.Body.Close()
	var item driveItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return api.FileInfo{}, err
	}
	return item.toFileInfo(p), nil
}

func (d *OneDriveDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}
	p := normPath(path)
	next := d.itemURL(p, "/children") + "?$top=1000"

	var out []api.FileInfo
	for next != "" {
		resp, err := d.do(context.Background(), "GET", next, nil, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Value    []driveItem `json:"value"`
			NextLink string      `json:"@odata.nextLink"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for i := range page.Value {
			child := &page.Value[i]
			childPath := joinPath(p, child.Name)
			out = append(out, child.toFileInfo(childPath))
		}
		next = page.NextLink
	}
	return out, nil
}

// OpenFile streams the file contents. The Graph /content endpoint responds
// with a 302 redirect to a pre-signed download URL; http.Client follows it
// automatically. The returned body belongs to the caller.
func (d *OneDriveDriver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}
	resp, err := d.do(context.Background(), "GET", d.itemURL(normPath(path), "/content"), nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (d *OneDriveDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}
	return &uploadWriter{driver: d, path: normPath(path)}, nil
}

func (d *OneDriveDriver) Mkdir(mountID int, path string) error {
	if !d.connected {
		return api.ErrNotConnected
	}
	p := normPath(path)
	parent, name := splitParent(p)
	body, _ := json.Marshal(map[string]interface{}{
		"name":                              name,
		"folder":                            struct{}{},
		"@microsoft.graph.conflictBehavior": "fail",
	})
	resp, err := d.do(context.Background(), "POST", d.itemURL(parent, "/children"),
		bytes.NewReader(body), jsonHeader)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (d *OneDriveDriver) Remove(mountID int, path string) error {
	if !d.connected {
		return api.ErrNotConnected
	}
	resp, err := d.do(context.Background(), "DELETE", d.itemURL(normPath(path), ""), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Rename handles both in-place renames and cross-folder moves via a single
// PATCH. When the parent folder changes, parentReference.path is included.
func (d *OneDriveDriver) Rename(mountID int, oldPath, newPath string) error {
	if !d.connected {
		return api.ErrNotConnected
	}
	op := normPath(oldPath)
	np := normPath(newPath)
	oldParent, _ := splitParent(op)
	newParent, newName := splitParent(np)

	payload := map[string]interface{}{"name": newName}
	if oldParent != newParent {
		payload["parentReference"] = map[string]interface{}{
			"path": "/drive/root" + graphPath(newParent),
		}
	}
	body, _ := json.Marshal(payload)
	resp, err := d.do(context.Background(), "PATCH", d.itemURL(op, ""),
		bytes.NewReader(body), jsonHeader)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// --- driveItem ------------------------------------------------------------

type driveItem struct {
	Name                 string `json:"name"`
	Size                 int64  `json:"size"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	Folder               *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder,omitempty"`
	File *struct {
		MimeType string `json:"mimeType"`
	} `json:"file,omitempty"`
}

func (it *driveItem) toFileInfo(path string) api.FileInfo {
	fi := api.FileInfo{
		Name:  it.Name,
		Path:  path,
		IsDir: it.Folder != nil,
		Size:  it.Size,
	}
	if fi.IsDir {
		fi.Size = 0
	}
	if t, err := time.Parse(time.RFC3339, it.LastModifiedDateTime); err == nil {
		fi.ModTime = t.Unix()
	}
	return fi
}

// --- uploadWriter ---------------------------------------------------------

// uploadWriter buffers up to uploadMemCap bytes in memory. Once that cap
// is exceeded it spills to a temp file and, on Close, ships the data via
// a Graph upload session. This keeps tiny writes fast and arbitrarily
// large writes memory-safe.
type uploadWriter struct {
	driver *OneDriveDriver
	path   string

	buf    bytes.Buffer
	temp   *os.File
	tempSz int64

	closed bool
	err    error
}

func (w *uploadWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("onedrive: write on closed writer")
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.temp != nil {
		n, err := w.temp.Write(p)
		w.tempSz += int64(n)
		return n, err
	}
	if w.buf.Len()+len(p) <= uploadMemCap {
		return w.buf.Write(p)
	}
	// Spill: flush in-memory buffer to a new temp file, then continue to disk.
	tmp, err := os.CreateTemp("", "onedrive-upload-*")
	if err != nil {
		w.err = err
		return 0, err
	}
	if _, err := tmp.Write(w.buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		w.err = err
		return 0, err
	}
	w.tempSz = int64(w.buf.Len())
	w.buf.Reset()
	w.temp = tmp
	n, err := w.temp.Write(p)
	w.tempSz += int64(n)
	return n, err
}

func (w *uploadWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.err != nil {
		w.cleanupTemp()
		return w.err
	}
	defer w.cleanupTemp()

	if w.temp == nil {
		return w.driver.putSmall(context.Background(), w.path, w.buf.Bytes())
	}
	if _, err := w.temp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return w.driver.uploadSession(context.Background(), w.path, w.temp, w.tempSz)
}

func (w *uploadWriter) cleanupTemp() {
	if w.temp != nil {
		name := w.temp.Name()
		w.temp.Close()
		os.Remove(name)
		w.temp = nil
	}
}

// putSmall uploads <= uploadMemCap bytes in a single PUT. Graph supports
// up to ~4 MiB via this endpoint; beyond that an upload session is required.
func (d *OneDriveDriver) putSmall(ctx context.Context, path string, data []byte) error {
	u := d.itemURL(path, "/content")
	resp, err := d.do(ctx, "PUT", u, bytes.NewReader(data), http.Header{
		"Content-Type": {"application/octet-stream"},
	})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// uploadSession streams a known-length body through a Graph upload session.
// Chunks are chunkSize bytes except the final one, which may be smaller.
// A single reusable buffer is held for the duration of the upload.
func (d *OneDriveDriver) uploadSession(ctx context.Context, path string, body io.Reader, total int64) error {
	sessionBody, _ := json.Marshal(map[string]interface{}{
		"item": map[string]interface{}{
			"@microsoft.graph.conflictBehavior": "replace",
		},
	})
	resp, err := d.do(ctx, "POST", d.itemURL(path, "/createUploadSession"),
		bytes.NewReader(sessionBody), jsonHeader)
	if err != nil {
		return err
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	err = json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if session.UploadURL == "" {
		return fmt.Errorf("onedrive: upload session missing uploadUrl")
	}

	buf := make([]byte, chunkSize)
	var sent int64
	for sent < total {
		want := int64(chunkSize)
		if remaining := total - sent; remaining < want {
			want = remaining
		}
		if _, err := io.ReadFull(body, buf[:want]); err != nil {
			d.cancelUploadSession(session.UploadURL)
			return err
		}
		if err := d.putChunk(ctx, session.UploadURL, buf[:want], sent, total); err != nil {
			d.cancelUploadSession(session.UploadURL)
			return err
		}
		sent += want
	}
	return nil
}

// putChunk uploads a single chunk with a bytes a-b/total Content-Range header.
// Chunk uploads go directly to the Azure-backed uploadUrl, not the Graph API,
// so they must NOT carry an Authorization header.
func (d *OneDriveDriver) putChunk(ctx context.Context, uploadURL string, chunk []byte, start, total int64) error {
	end := start + int64(len(chunk)) - 1
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "PUT", uploadURL, bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		req.ContentLength = int64(len(chunk))
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := d.httpClient.Do(req)
		if err != nil {
			if attempt < maxRetries {
				backoff(attempt)
				continue
			}
			return err
		}
		status := resp.StatusCode
		if status == 200 || status == 201 || status == 202 {
			resp.Body.Close()
			return nil
		}
		if shouldRetry(status) && attempt < maxRetries {
			waitForRetry(resp, attempt)
			resp.Body.Close()
			continue
		}
		return newHTTPError(resp)
	}
	return fmt.Errorf("onedrive: chunk upload exhausted retries")
}

func (d *OneDriveDriver) cancelUploadSession(uploadURL string) {
	req, err := http.NewRequest("DELETE", uploadURL, nil)
	if err != nil {
		return
	}
	resp, err := d.httpClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// --- HTTP / auth ----------------------------------------------------------

var jsonHeader = http.Header{"Content-Type": {"application/json"}}

// do is the single entry point for authenticated Graph calls. It handles
// proactive token refresh, 401-driven refresh + retry, 429 Retry-After,
// and 5xx backoff. For requests with a body, the body must be seekable
// (bytes.Reader is — io.Reader is not, so we require a ReadSeeker-capable
// source at the call site).
func (d *OneDriveDriver) do(ctx context.Context, method, url string, body io.Reader, extra http.Header) (*http.Response, error) {
	var bodyReader *bytes.Reader
	if body != nil {
		// All internal callers pass *bytes.Reader; this is a tight, deliberate
		// contract so we can rewind on retry without extra allocations.
		br, ok := body.(*bytes.Reader)
		if !ok {
			return nil, fmt.Errorf("onedrive: internal error - body must be *bytes.Reader for retryable requests")
		}
		bodyReader = br
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := d.ensureToken(ctx); err != nil {
			return nil, err
		}
		if bodyReader != nil {
			bodyReader.Seek(0, io.SeekStart)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bodyReaderOrNil(bodyReader))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+d.token())
		for k, vs := range extra {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}

		resp, err := d.httpClient.Do(req)
		if err != nil {
			if attempt < maxRetries {
				backoff(attempt)
				continue
			}
			return nil, err
		}
		if resp.StatusCode < 400 {
			return resp, nil
		}
		if resp.StatusCode == 401 && attempt == 0 {
			resp.Body.Close()
			if err := d.refresh(ctx); err != nil {
				return nil, err
			}
			continue
		}
		if shouldRetry(resp.StatusCode) && attempt < maxRetries {
			waitForRetry(resp, attempt)
			resp.Body.Close()
			continue
		}
		err = newHTTPError(resp)
		return nil, err
	}
	return nil, fmt.Errorf("onedrive: exhausted retries for %s %s", method, url)
}

func bodyReaderOrNil(r *bytes.Reader) io.Reader {
	if r == nil {
		return nil
	}
	return r
}

func (d *OneDriveDriver) token() string {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	return d.accessToken
}

// ensureToken refreshes proactively when within 60 s of expiry.
func (d *OneDriveDriver) ensureToken(ctx context.Context) error {
	d.tokenMu.Lock()
	stale := d.accessToken == "" || time.Until(d.expiresAt) < 60*time.Second
	d.tokenMu.Unlock()
	if !stale {
		return nil
	}
	return d.refresh(ctx)
}

func (d *OneDriveDriver) refresh(ctx context.Context) error {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {d.refreshToken},
		"client_id":     {d.clientID},
	}
	if d.clientSecret != "" {
		form.Set("client_secret", d.clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token refresh HTTP %d: %s", resp.StatusCode, string(b))
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if result.AccessToken == "" {
		return fmt.Errorf("token refresh returned empty access_token")
	}

	d.tokenMu.Lock()
	d.accessToken = result.AccessToken
	if result.ExpiresIn > 0 {
		d.expiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	} else {
		d.expiresAt = time.Now().Add(55 * time.Minute)
	}
	// Microsoft may rotate the refresh token.
	if result.RefreshToken != "" {
		d.refreshToken = result.RefreshToken
	}
	d.tokenMu.Unlock()
	return nil
}

func shouldRetry(status int) bool {
	return status == 429 || status == 502 || status == 503 || status == 504
}

func waitForRetry(resp *http.Response, attempt int) {
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.Atoi(h); err == nil && secs > 0 && secs <= 120 {
			time.Sleep(time.Duration(secs) * time.Second)
			return
		}
	}
	backoff(attempt)
}

func backoff(attempt int) {
	// 0.5 s, 1 s, 2 s, 4 s.
	d := time.Duration(500*(1<<attempt)) * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	time.Sleep(d)
}

func newHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	switch resp.StatusCode {
	case 404:
		return api.ErrNotFound
	case 403:
		return api.ErrPermissionDenied
	case 409:
		return api.ErrExists
	}
	return fmt.Errorf("onedrive: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

// --- path helpers ---------------------------------------------------------

// graphPath returns the Graph "root:/..." infix for an internal path.
// Empty string for root, so callers can distinguish /me/drive/root from
// /me/drive/root:/something. Each segment is escaped individually so that
// slashes remain literal separators.
func graphPath(path string) string {
	trimmed := strings.Trim(normPath(path), "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return ":/" + strings.Join(parts, "/")
}

// itemURL builds a Graph endpoint for a given internal path and suffix.
// suffix is "" for metadata, "/children", "/content", "/createUploadSession".
func (d *OneDriveDriver) itemURL(path, suffix string) string {
	gp := graphPath(path)
	if gp == "" {
		return graphBase + "/me/drive/root" + suffix
	}
	if suffix == "" {
		return graphBase + "/me/drive/root" + gp
	}
	return graphBase + "/me/drive/root" + gp + ":" + suffix
}

func normPath(path string) string {
	if path == "" || path == "/" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(path, "/")
}

func joinPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

func splitParent(path string) (parent, name string) {
	p := normPath(path)
	if p == "/" {
		return "/", ""
	}
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return "/", p[1:]
	}
	return p[:idx], p[idx+1:]
}
