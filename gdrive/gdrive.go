// gdrive/gdrive.go - Google Drive filesystem driver
//
// This package implements the api.Driver interface for Google Drive
// via the Drive v3 REST API. Authentication uses the OAuth2 refresh
// token flow — callers supply client_id, client_secret, and a
// refresh_token obtained out-of-band.
//
// Adapted from the ext4-fskit gdrivebridge C-bridge into the unified
// go-networkfs Driver shape (dropbox.go is the template).

package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// Driver type ID - must match dispatcher registry
const DriverTypeID = 6

func init() {
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &GDriveDriver{}
	})
}

// GDriveDriver implements the Driver interface for Google Drive.
type GDriveDriver struct {
	connected bool

	clientID     string
	clientSecret string
	refreshToken string

	tokenMu     sync.Mutex
	accessToken string

	httpClient *http.Client

	// path -> Drive file ID cache. Seeded with root aliases.
	cacheMu   sync.Mutex
	pathCache map[string]string
}

// Name returns the driver identifier.
func (d *GDriveDriver) Name() string {
	return "gdrive"
}

// Mount initialises the OAuth2 client and verifies/refreshes the access token.
//
// Config keys:
//
//	client_id      - OAuth2 client ID (required)
//	client_secret  - OAuth2 client secret (required)
//	refresh_token  - long-lived refresh token (required)
//	access_token   - optional short-lived token; refreshed on first 401
func (d *GDriveDriver) Mount(mountID int, config map[string]string) error {
	clientID := config["client_id"]
	clientSecret := config["client_secret"]
	refreshToken := config["refresh_token"]

	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return &api.DriverError{Code: 10, Message: "gdrive: client_id, client_secret, and refresh_token are required"}
	}

	d.clientID = clientID
	d.clientSecret = clientSecret
	d.refreshToken = refreshToken
	d.accessToken = config["access_token"]
	d.httpClient = &http.Client{Timeout: 30 * time.Second}
	d.pathCache = map[string]string{"/": "root", "": "root"}

	if d.accessToken == "" {
		if err := d.doRefresh(); err != nil {
			return &api.DriverError{Code: 12, Message: "gdrive: initial token refresh failed: " + err.Error()}
		}
	} else if err := d.validateToken(); err != nil {
		if err := d.doRefresh(); err != nil {
			return &api.DriverError{Code: 12, Message: "gdrive: token validation and refresh failed: " + err.Error()}
		}
	}

	d.connected = true
	return nil
}

// Unmount clears caches. No persistent connection to tear down.
func (d *GDriveDriver) Unmount(mountID int) error {
	d.cacheMu.Lock()
	d.pathCache = nil
	d.cacheMu.Unlock()
	d.connected = false
	return nil
}

// Stat retrieves file/directory metadata.
func (d *GDriveDriver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected {
		return api.FileInfo{}, api.ErrNotConnected
	}

	p := normPath(path)
	if p == "/" {
		return api.FileInfo{Name: "", Path: "/", IsDir: true}, nil
	}

	driveID, err := d.resolvePath(p)
	if err != nil {
		return api.FileInfo{}, err
	}

	meta, err := d.apiGET(fmt.Sprintf(
		"https://www.googleapis.com/drive/v3/files/%s?fields=name,mimeType,size,modifiedTime",
		driveID))
	if err != nil {
		return api.FileInfo{}, err
	}

	info := api.FileInfo{
		Name: strOrDefault(meta["name"], nameFromPath(p)),
		Path: p,
	}
	if mt, _ := meta["mimeType"].(string); mt == folderMime {
		info.IsDir = true
	} else {
		if sizeStr, ok := meta["size"].(string); ok {
			fmt.Sscanf(sizeStr, "%d", &info.Size)
		}
	}
	if modStr, ok := meta["modifiedTime"].(string); ok {
		if t, err := time.Parse(time.RFC3339, modStr); err == nil {
			info.ModTime = t.Unix()
		}
	}
	return info, nil
}

// ListDir returns entries in a directory.
func (d *GDriveDriver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}

	p := normPath(path)
	parentID, err := d.resolvePath(p)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf("'%s' in parents and trashed = false", parentID)
	fields := "nextPageToken,files(id,name,mimeType,size,modifiedTime)"
	pageToken := ""

	var out []api.FileInfo
	for {
		urlStr := fmt.Sprintf(
			"https://www.googleapis.com/drive/v3/files?q=%s&fields=%s&pageSize=1000",
			url.QueryEscape(query), url.QueryEscape(fields))
		if pageToken != "" {
			urlStr += "&pageToken=" + url.QueryEscape(pageToken)
		}

		res, err := d.apiGET(urlStr)
		if err != nil {
			return nil, err
		}

		filesArr, _ := res["files"].([]interface{})
		for _, f := range filesArr {
			obj, ok := f.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := obj["name"].(string)
			mimeType, _ := obj["mimeType"].(string)
			childID, _ := obj["id"].(string)

			childPath := strings.TrimRight(p, "/") + "/" + name
			if p == "/" {
				childPath = "/" + name
			}
			d.cachePut(childPath, childID)

			entry := api.FileInfo{
				Name:  name,
				Path:  childPath,
				IsDir: mimeType == folderMime,
			}
			if sizeStr, ok := obj["size"].(string); ok {
				fmt.Sscanf(sizeStr, "%d", &entry.Size)
			}
			if modStr, ok := obj["modifiedTime"].(string); ok {
				if t, err := time.Parse(time.RFC3339, modStr); err == nil {
					entry.ModTime = t.Unix()
				}
			}
			out = append(out, entry)
		}

		nextToken, _ := res["nextPageToken"].(string)
		if nextToken == "" {
			break
		}
		pageToken = nextToken
	}
	return out, nil
}

// OpenFile returns a reader for file contents. Google Docs types are
// exported (PDF/CSV/PNG) since they are not directly downloadable.
func (d *GDriveDriver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}

	p := normPath(path)
	driveID, err := d.resolvePath(p)
	if err != nil {
		return nil, err
	}

	meta, err := d.apiGET(fmt.Sprintf(
		"https://www.googleapis.com/drive/v3/files/%s?fields=mimeType", driveID))
	if err != nil {
		return nil, err
	}
	mimeType, _ := meta["mimeType"].(string)

	var urlStr string
	if strings.HasPrefix(mimeType, "application/vnd.google-apps.") {
		urlStr = fmt.Sprintf(
			"https://www.googleapis.com/drive/v3/files/%s/export?mimeType=%s",
			driveID, url.QueryEscape(exportMimeFor(mimeType)))
	} else {
		urlStr = fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?alt=media", driveID)
	}

	resp, err := d.apiRawGET(urlStr)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// CreateFile returns a buffered writer that uploads on Close().
// Drive's multipart upload requires the content up-front, so writes are
// buffered in memory (same pattern as the Dropbox driver).
type gdriveWriter struct {
	buf    bytes.Buffer
	driver *GDriveDriver
	path   string
	closed bool
}

func (w *gdriveWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("writer closed")
	}
	return w.buf.Write(p)
}

func (w *gdriveWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.driver.uploadFile(w.path, w.buf.Bytes())
}

func (d *GDriveDriver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected {
		return nil, api.ErrNotConnected
	}
	return &gdriveWriter{driver: d, path: normPath(path)}, nil
}

// Mkdir creates a directory.
func (d *GDriveDriver) Mkdir(mountID int, path string) error {
	if !d.connected {
		return api.ErrNotConnected
	}

	p := normPath(path)
	parent, name := splitParent(p)
	parentID, err := d.resolvePath(parent)
	if err != nil {
		return err
	}

	body, _ := json.Marshal(map[string]interface{}{
		"name":     name,
		"mimeType": folderMime,
		"parents":  []string{parentID},
	})
	res, err := d.apiJSON("POST",
		"https://www.googleapis.com/drive/v3/files?fields=id",
		body)
	if err != nil {
		return err
	}
	if id, _ := res["id"].(string); id != "" {
		d.cachePut(p, id)
	}
	return nil
}

// Remove deletes a file or directory (moves to trash in Drive terms,
// but Drive's DELETE permanently removes — we use DELETE for symmetry
// with other drivers).
func (d *GDriveDriver) Remove(mountID int, path string) error {
	if !d.connected {
		return api.ErrNotConnected
	}

	p := normPath(path)
	driveID, err := d.resolvePath(p)
	if err != nil {
		return err
	}

	if err := d.apiDELETE(fmt.Sprintf(
		"https://www.googleapis.com/drive/v3/files/%s", driveID)); err != nil {
		return err
	}
	d.cacheDelete(p)
	return nil
}

// Rename moves and/or renames a file or directory. Drive requires a
// PATCH with addParents/removeParents when the parent changes.
func (d *GDriveDriver) Rename(mountID int, oldPath, newPath string) error {
	if !d.connected {
		return api.ErrNotConnected
	}

	op := normPath(oldPath)
	np := normPath(newPath)

	driveID, err := d.resolvePath(op)
	if err != nil {
		return err
	}

	oldParent, _ := splitParent(op)
	newParent, newName := splitParent(np)

	urlStr := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?fields=id", driveID)

	if oldParent != newParent {
		oldParentID, err := d.resolvePath(oldParent)
		if err != nil {
			return err
		}
		newParentID, err := d.resolvePath(newParent)
		if err != nil {
			return err
		}
		urlStr += "&addParents=" + url.QueryEscape(newParentID) +
			"&removeParents=" + url.QueryEscape(oldParentID)
	}

	body, _ := json.Marshal(map[string]interface{}{"name": newName})
	if _, err := d.apiJSON("PATCH", urlStr, body); err != nil {
		return err
	}

	d.cacheDelete(op)
	d.cachePut(np, driveID)
	return nil
}

// --- internal helpers ------------------------------------------------------

const folderMime = "application/vnd.google-apps.folder"

func exportMimeFor(mimeType string) string {
	switch mimeType {
	case "application/vnd.google-apps.spreadsheet":
		return "text/csv"
	case "application/vnd.google-apps.drawing":
		return "image/png"
	default:
		return "application/pdf"
	}
}

func (d *GDriveDriver) cachePut(path, id string) {
	d.cacheMu.Lock()
	if d.pathCache == nil {
		d.pathCache = map[string]string{"/": "root", "": "root"}
	}
	d.pathCache[path] = id
	d.cacheMu.Unlock()
}

func (d *GDriveDriver) cacheGet(path string) (string, bool) {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	id, ok := d.pathCache[path]
	return id, ok
}

func (d *GDriveDriver) cacheDelete(path string) {
	d.cacheMu.Lock()
	delete(d.pathCache, path)
	d.cacheMu.Unlock()
}

func (d *GDriveDriver) validateToken() error {
	d.tokenMu.Lock()
	tok := d.accessToken
	d.tokenMu.Unlock()

	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://www.googleapis.com/oauth2/v1/tokeninfo?access_token="+url.QueryEscape(tok), nil)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("token invalid (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func (d *GDriveDriver) doRefresh() error {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {d.refreshToken},
		"client_id":     {d.clientID},
		"client_secret": {d.clientSecret},
	}
	resp, err := d.httpClient.PostForm("https://oauth2.googleapis.com/token", data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	accessToken, err := parseTokenResponse(resp.StatusCode, body)
	if err != nil {
		return err
	}
	d.tokenMu.Lock()
	d.accessToken = accessToken
	d.tokenMu.Unlock()
	return nil
}

// parseTokenResponse is the pure kernel of doRefresh: given the HTTP
// status and raw body from Google's /token endpoint, it returns either
// the access_token string or a formatted error. Broken out so the
// success path (JSON decode) and failure path (status + body format)
// can be tested directly without spinning up an httptest OAuth server.
//
// Wire spec:
//   200 + body with {"access_token": "..."} → returns the token
//   non-200                                   → error "refresh HTTP <code>: <body>"
//   200 + malformed JSON                      → JSON decode error
//   200 + valid JSON but empty access_token   → returns "" and nil (caller
//                                                can choose to reject if it cares)
func parseTokenResponse(status int, body []byte) (string, error) {
	if status != 200 {
		return "", fmt.Errorf("refresh HTTP %d: %s", status, string(body))
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

func (d *GDriveDriver) authHeader() string {
	d.tokenMu.Lock()
	tok := d.accessToken
	d.tokenMu.Unlock()
	return "Bearer " + tok
}

// shouldRefreshAndRetry is the pure predicate for "does this response
// warrant a token refresh + single retry?". Today only 401 qualifies —
// broken out so the four API helpers (apiRawGET / apiGET / apiJSON /
// apiDELETE) share the same decision instead of each open-coding it.
func shouldRefreshAndRetry(status int) bool {
	return status == 401
}

// formatAPIError is the pure formatter for non-2xx Google Drive API
// responses. Called after the 401 retry dance is over, so a 401 here
// means "even after refresh the request still failed", not "needs
// refresh". Shape matches the prior inline fmt.Errorf so no caller
// sees a different error string.
func formatAPIError(status int, body []byte) error {
	return fmt.Errorf("API HTTP %d: %s", status, string(body))
}

// apiRawGET issues an authorized GET and returns the raw response. The
// caller owns resp.Body. Retries once on 401 after refreshing the token.
func (d *GDriveDriver) apiRawGET(urlStr string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", d.authHeader())

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if shouldRefreshAndRetry(resp.StatusCode) {
		resp.Body.Close()
		if err := d.doRefresh(); err != nil {
			return nil, fmt.Errorf("401 and refresh failed: %w", err)
		}
		req2, _ := http.NewRequestWithContext(context.Background(), "GET", urlStr, nil)
		req2.Header.Set("Authorization", d.authHeader())
		resp, err = d.httpClient.Do(req2)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, formatAPIError(resp.StatusCode, body)
	}
	return resp, nil
}

// apiGET issues an authorized GET and decodes a JSON object response.
func (d *GDriveDriver) apiGET(urlStr string) (map[string]interface{}, error) {
	resp, err := d.apiRawGET(urlStr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

// apiJSON issues an authorized JSON-body request (POST/PATCH) and decodes
// the JSON response. Retries once on 401 after refreshing the token.
func (d *GDriveDriver) apiJSON(method, urlStr string, body []byte) (map[string]interface{}, error) {
	do := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(context.Background(), method, urlStr, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", d.authHeader())
		req.Header.Set("Content-Type", "application/json")
		return d.httpClient.Do(req)
	}

	resp, err := do()
	if err != nil {
		return nil, err
	}
	if shouldRefreshAndRetry(resp.StatusCode) {
		resp.Body.Close()
		if err := d.doRefresh(); err != nil {
			return nil, fmt.Errorf("401 and refresh failed: %w", err)
		}
		resp, err = do()
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, formatAPIError(resp.StatusCode, b)
	}

	var result map[string]interface{}
	if resp.ContentLength == 0 {
		return result, nil
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

// apiDELETE issues an authorized DELETE. Retries once on 401.
func (d *GDriveDriver) apiDELETE(urlStr string) error {
	do := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(context.Background(), "DELETE", urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", d.authHeader())
		return d.httpClient.Do(req)
	}

	resp, err := do()
	if err != nil {
		return err
	}
	if shouldRefreshAndRetry(resp.StatusCode) {
		resp.Body.Close()
		if err := d.doRefresh(); err != nil {
			return fmt.Errorf("401 and refresh failed: %w", err)
		}
		resp, err = do()
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != 204 {
		body, _ := io.ReadAll(resp.Body)
		return formatAPIError(resp.StatusCode, body)
	}
	return nil
}

// uploadFile performs a multipart upload, replacing an existing file at
// the same path if one exists, otherwise creating a new one.
func (d *GDriveDriver) uploadFile(path string, data []byte) error {
	p := normPath(path)

	existingID := ""
	if id, err := d.resolvePath(p); err == nil {
		existingID = id
	}

	parent, name := splitParent(p)
	parentID, err := d.resolvePath(parent)
	if err != nil {
		return err
	}

	var meta map[string]interface{}
	var urlStr, method string
	if existingID != "" {
		meta = map[string]interface{}{"name": name}
		urlStr = fmt.Sprintf(
			"https://www.googleapis.com/upload/drive/v3/files/%s?uploadType=multipart&fields=id",
			existingID)
		method = "PATCH"
	} else {
		meta = map[string]interface{}{"name": name, "parents": []string{parentID}}
		urlStr = "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id"
		method = "POST"
	}

	metaJSON, _ := json.Marshal(meta)

	var body bytes.Buffer
	const boundary = "gdrive-boundary-7e9d4c2a"
	fmt.Fprintf(&body, "--%s\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n%s\r\n",
		boundary, metaJSON)
	fmt.Fprintf(&body, "--%s\r\nContent-Type: application/octet-stream\r\n\r\n", boundary)
	body.Write(data)
	fmt.Fprintf(&body, "\r\n--%s--\r\n", boundary)

	do := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(context.Background(), method, urlStr,
			bytes.NewReader(body.Bytes()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", d.authHeader())
		req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)
		return d.httpClient.Do(req)
	}

	resp, err := do()
	if err != nil {
		return err
	}
	if shouldRefreshAndRetry(resp.StatusCode) {
		resp.Body.Close()
		if err := d.doRefresh(); err != nil {
			return fmt.Errorf("401 and refresh failed: %w", err)
		}
		resp, err = do()
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		// uploadFile keeps its own "upload HTTP ..." prefix rather than
		// the generic "API HTTP ..." — callers distinguish upload
		// failures from plain API failures by the string.
		return fmt.Errorf("upload HTTP %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.ID != "" {
		d.cachePut(p, result.ID)
	}
	return nil
}

// resolvePath walks the path segment-by-segment, consulting the cache
// and falling back to Drive queries by (parent, name).
func (d *GDriveDriver) resolvePath(path string) (string, error) {
	p := normPath(path)
	if p == "/" || p == "" {
		return "root", nil
	}
	if id, ok := d.cacheGet(p); ok {
		return id, nil
	}

	components := strings.Split(strings.TrimPrefix(p, "/"), "/")
	currentID := "root"
	builtPath := ""

	for _, comp := range components {
		builtPath += "/" + comp
		if cached, ok := d.cacheGet(builtPath); ok {
			currentID = cached
			continue
		}

		escaped := strings.ReplaceAll(comp, "'", "\\'")
		query := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false", currentID, escaped)
		urlStr := fmt.Sprintf(
			"https://www.googleapis.com/drive/v3/files?q=%s&fields=files(id)&pageSize=1",
			url.QueryEscape(query))

		result, err := d.apiGET(urlStr)
		if err != nil {
			return "", err
		}
		filesArr, _ := result["files"].([]interface{})
		if len(filesArr) == 0 {
			return "", &api.DriverError{Code: 5, Message: "not found: " + builtPath}
		}
		obj, _ := filesArr[0].(map[string]interface{})
		childID, _ := obj["id"].(string)
		if childID == "" {
			return "", fmt.Errorf("no id for %s", builtPath)
		}

		d.cachePut(builtPath, childID)
		currentID = childID
	}

	return currentID, nil
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

func nameFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

func strOrDefault(v interface{}, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}
