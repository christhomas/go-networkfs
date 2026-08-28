package gdrive

// Tests driven by a stand-in for the Drive API.
//
// Almost all of this driver is HTTP: a token refresh, a path resolved one
// component at a time by querying for each child by name, then requests built
// from driveBase and responses decoded from JSON. None of it runs without a
// server, which is why the path cache, the pagination, the Google-Docs export
// path and the upload were previously untested.
//
// The fake records what it was asked, so the tests can assert on the request —
// which matters most for resolvePath, whose whole job is the sequence of
// queries it issues and the cache it builds from them.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type apiCall struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
}

type fakeDrive struct {
	t        *testing.T
	server   *httptest.Server
	calls    []apiCall
	handlers map[string]http.HandlerFunc
	tokens   int
}

func newFakeDrive(t *testing.T) *fakeDrive {
	t.Helper()
	f := &fakeDrive{t: t, handlers: map[string]http.HandlerFunc{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.calls = append(f.calls, apiCall{
			Method: r.Method, Path: r.URL.Path,
			Query: r.URL.Query(), Body: string(body),
		})
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		if h, ok := f.handlers[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		switch r.URL.Path {
		case "/token":
			f.tokens++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, f.tokens)
			return
		case "/tokeninfo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"expires_in":3600}`)
			return
		}
		http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	oldDrive, oldUpload, oldInfo, oldToken := driveBase, uploadBase, tokenInfoURL, tokenURL
	driveBase = f.server.URL + "/drive/v3"
	uploadBase = f.server.URL + "/upload/drive/v3"
	tokenInfoURL = f.server.URL + "/tokeninfo"
	tokenURL = f.server.URL + "/token"
	t.Cleanup(func() {
		driveBase, uploadBase, tokenInfoURL, tokenURL = oldDrive, oldUpload, oldInfo, oldToken
	})

	return f
}

func (f *fakeDrive) json(methodAndPath string, status int, body string) {
	f.handlers[methodAndPath] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func (f *fakeDrive) handle(methodAndPath string, h http.HandlerFunc) {
	f.handlers[methodAndPath] = h
}

// listQueries returns the q= parameter of every /files listing call, in order.
// resolvePath is defined by this sequence.
func (f *fakeDrive) listQueries() []string {
	var out []string
	for _, c := range f.calls {
		if c.Method == "GET" && c.Path == "/drive/v3/files" {
			if q := c.Query.Get("q"); q != "" {
				out = append(out, q)
			}
		}
	}
	return out
}

func mountFake(t *testing.T, f *fakeDrive) *GDriveDriver {
	t.Helper()
	d := &GDriveDriver{}
	err := d.Mount(1, map[string]string{
		"client_id":     "cid",
		"client_secret": "csec",
		"refresh_token": "rtok",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })
	return d
}

// filesResponse builds a listing body from id/name/mime triples.
func filesResponse(items ...[3]string) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, fmt.Sprintf(
			`{"id":%q,"name":%q,"mimeType":%q,"size":"7","modifiedTime":"2026-01-02T03:04:05Z"}`,
			it[0], it[1], it[2]))
	}
	return `{"files":[` + strings.Join(parts, ",") + `]}`
}

func TestMountRefreshesWhenNoAccessToken(t *testing.T) {
	f := newFakeDrive(t)
	mountFake(t, f)

	if f.tokens != 1 {
		t.Errorf("minted %d tokens, want 1", f.tokens)
	}
}

func TestMountValidatesSuppliedAccessToken(t *testing.T) {
	f := newFakeDrive(t)

	d := &GDriveDriver{}
	err := d.Mount(1, map[string]string{
		"client_id": "cid", "client_secret": "csec",
		"refresh_token": "rtok", "access_token": "supplied",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })

	// A valid supplied token means no refresh is needed.
	if f.tokens != 0 {
		t.Errorf("minted %d tokens despite a valid supplied token", f.tokens)
	}
}

func TestMountRefreshesWhenSuppliedTokenIsRejected(t *testing.T) {
	f := newFakeDrive(t)
	f.json("GET /tokeninfo", http.StatusBadRequest, `{"error":"invalid_token"}`)

	d := &GDriveDriver{}
	err := d.Mount(1, map[string]string{
		"client_id": "cid", "client_secret": "csec",
		"refresh_token": "rtok", "access_token": "stale",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })

	if f.tokens != 1 {
		t.Errorf("minted %d tokens, want 1 after the stale token was rejected", f.tokens)
	}
}

func TestMountFailsWhenRefreshFails(t *testing.T) {
	f := newFakeDrive(t)
	f.json("POST /token", http.StatusUnauthorized, `{"error":"invalid_grant"}`)

	d := &GDriveDriver{}
	err := d.Mount(1, map[string]string{
		"client_id": "cid", "client_secret": "csec", "refresh_token": "bad",
	})
	if err == nil {
		_ = d.Unmount(1)
		t.Fatal("mount succeeded with a rejected refresh token")
	}
}

// A path is resolved one component at a time, each by a name query scoped to
// the previous component's id.
func TestResolvePathWalksComponents(t *testing.T) {
	f := newFakeDrive(t)
	f.handle("GET /drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "'root' in parents") && strings.Contains(q, "name = 'docs'"):
			_, _ = io.WriteString(w, `{"files":[{"id":"id-docs"}]}`)
		case strings.Contains(q, "'id-docs' in parents") && strings.Contains(q, "name = 'a.txt'"):
			_, _ = io.WriteString(w, `{"files":[{"id":"id-a"}]}`)
		default:
			_, _ = io.WriteString(w, `{"files":[]}`)
		}
	})
	f.json("GET /drive/v3/files/id-a", http.StatusOK,
		`{"name":"a.txt","mimeType":"text/plain","size":"7","modifiedTime":"2026-01-02T03:04:05Z"}`)
	d := mountFake(t, f)

	fi, err := d.Stat(1, "/docs/a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name != "a.txt" || fi.IsDir {
		t.Errorf("unexpected info %+v", fi)
	}

	queries := f.listQueries()
	if len(queries) != 2 {
		t.Fatalf("resolution issued %d queries, want 2: %v", len(queries), queries)
	}
	if !strings.Contains(queries[0], "name = 'docs'") {
		t.Errorf("first query does not look up docs: %s", queries[0])
	}
	if !strings.Contains(queries[1], "'id-docs' in parents") {
		t.Errorf("second query is not scoped to the first result: %s", queries[1])
	}
}

// The path cache exists so a second lookup costs no requests.
func TestResolvePathCachesComponents(t *testing.T) {
	f := newFakeDrive(t)
	f.json("GET /drive/v3/files", http.StatusOK, `{"files":[{"id":"id-docs"}]}`)
	f.json("GET /drive/v3/files/id-docs", http.StatusOK,
		`{"name":"docs","mimeType":"application/vnd.google-apps.folder"}`)
	d := mountFake(t, f)

	if _, err := d.Stat(1, "/docs"); err != nil {
		t.Fatalf("first Stat: %v", err)
	}
	afterFirst := len(f.listQueries())

	if _, err := d.Stat(1, "/docs"); err != nil {
		t.Fatalf("second Stat: %v", err)
	}
	if got := len(f.listQueries()); got != afterFirst {
		t.Errorf("second Stat issued %d extra lookups, cache not used", got-afterFirst)
	}
}

func TestStatRootNeedsNoRequest(t *testing.T) {
	f := newFakeDrive(t)
	d := mountFake(t, f)
	before := len(f.calls)

	fi, err := d.Stat(1, "/")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir {
		t.Error("root not reported as a directory")
	}
	if len(f.calls) != before {
		t.Error("stat of root issued a request")
	}
}

func TestStatMissingPathErrors(t *testing.T) {
	f := newFakeDrive(t)
	f.json("GET /drive/v3/files", http.StatusOK, `{"files":[]}`)
	d := mountFake(t, f)

	if _, err := d.Stat(1, "/nope.txt"); err == nil {
		t.Error("Stat of a missing path succeeded")
	}
}

func TestListDirFollowsPageTokens(t *testing.T) {
	f := newFakeDrive(t)
	f.handle("GET /drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = io.WriteString(w, `{"nextPageToken":"page2","files":[`+
				`{"id":"1","name":"a.txt","mimeType":"text/plain","size":"7"}]}`)
			return
		}
		_, _ = io.WriteString(w, filesResponse(
			[3]string{"2", "b.txt", "text/plain"},
			[3]string{"3", "sub", "application/vnd.google-apps.folder"},
		))
	})
	d := mountFake(t, f)

	entries, err := d.ListDir(1, "/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries across two pages, want 3: %+v", len(entries), entries)
	}
	if entries[2].Name != "sub" || !entries[2].IsDir {
		t.Errorf("folder entry wrong: %+v", entries[2])
	}
	if entries[0].Path != "/a.txt" {
		t.Errorf("child path %q, want /a.txt", entries[0].Path)
	}
}

func TestOpenFileStreamsContent(t *testing.T) {
	f := newFakeDrive(t)
	f.json("GET /drive/v3/files", http.StatusOK, `{"files":[{"id":"id-a"}]}`)
	f.json("GET /drive/v3/files/id-a", http.StatusOK, `{"mimeType":"text/plain"}`)
	d := mountFake(t, f)

	// alt=media returns the bytes themselves rather than JSON.
	f.handle("GET /drive/v3/files/id-a", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			_, _ = io.WriteString(w, "file bytes")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"mimeType":"text/plain"}`)
	})

	rc, err := d.OpenFile(1, "/a.txt")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "file bytes" {
		t.Errorf("read %q, want %q", body, "file bytes")
	}
}

// A Google Docs file has no bytes of its own and must be exported instead.
func TestOpenFileExportsGoogleDoc(t *testing.T) {
	f := newFakeDrive(t)
	f.json("GET /drive/v3/files", http.StatusOK, `{"files":[{"id":"id-doc"}]}`)
	exported := false
	f.handle("GET /drive/v3/files/id-doc", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"mimeType":"application/vnd.google-apps.document"}`)
	})
	f.handle("GET /drive/v3/files/id-doc/export", func(w http.ResponseWriter, r *http.Request) {
		exported = true
		if mt := r.URL.Query().Get("mimeType"); mt == "" {
			t.Error("export called without a target mimeType")
		}
		_, _ = io.WriteString(w, "exported bytes")
	})
	d := mountFake(t, f)

	rc, err := d.OpenFile(1, "/doc")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)

	if !exported {
		t.Error("a Google Doc was not routed through export")
	}
	if string(body) != "exported bytes" {
		t.Errorf("read %q", body)
	}
}

func TestMkdirPostsFolderMetadata(t *testing.T) {
	f := newFakeDrive(t)
	f.json("POST /drive/v3/files", http.StatusOK, `{"id":"new-folder"}`)
	d := mountFake(t, f)

	if err := d.Mkdir(1, "/newdir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	var call *apiCall
	for i := range f.calls {
		if f.calls[i].Method == "POST" && f.calls[i].Path == "/drive/v3/files" {
			call = &f.calls[i]
		}
	}
	if call == nil {
		t.Fatal("no create request recorded")
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["name"] != "newdir" {
		t.Errorf("name %v, want newdir", body["name"])
	}
	if body["mimeType"] != "application/vnd.google-apps.folder" {
		t.Errorf("mimeType %v, not a folder", body["mimeType"])
	}
}

func TestRemoveDeletesResolvedID(t *testing.T) {
	f := newFakeDrive(t)
	f.json("GET /drive/v3/files", http.StatusOK, `{"files":[{"id":"id-gone"}]}`)
	f.json("DELETE /drive/v3/files/id-gone", http.StatusNoContent, "")
	d := mountFake(t, f)

	if err := d.Remove(1, "/gone.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, c := range f.calls {
		if c.Method == "DELETE" && c.Path == "/drive/v3/files/id-gone" {
			return
		}
	}
	t.Error("no delete of the resolved id")
}

func TestNotConnectedBeforeMount(t *testing.T) {
	d := &GDriveDriver{}
	if _, err := d.Stat(1, "/a"); err == nil {
		t.Error("Stat succeeded before Mount")
	}
	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir succeeded before Mount")
	}
}
