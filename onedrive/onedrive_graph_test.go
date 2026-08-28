package onedrive

// Tests driven by a stand-in for Microsoft Graph.
//
// Everything below Mount is HTTP: a token refresh, then requests built from
// graphBase and responses decoded into driveItem. None of that can be reached
// without a server, which is why the request building, the retry policy, the
// pagination and the upload paths were previously untested. The fake here
// speaks enough Graph to exercise them, and records what it was asked so the
// tests can assert on the request rather than only the result.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordedRequest is one call the driver made.
type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Body   string
	Header http.Header
}

// fakeGraph serves the subset of Graph this driver uses. Handlers are keyed by
// "METHOD /path"; anything unmatched is a 404 the test will notice.
type fakeGraph struct {
	t        *testing.T
	server   *httptest.Server
	requests []recordedRequest
	handlers map[string]http.HandlerFunc
	tokens   int // how many times a token was minted
}

func newFakeGraph(t *testing.T) *fakeGraph {
	t.Helper()
	g := &fakeGraph{t: t, handlers: map[string]http.HandlerFunc{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.requests = append(g.requests, recordedRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Body: string(body), Header: r.Header.Clone(),
		})
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		// An explicit handler wins, so a test can make the token endpoint
		// fail. Otherwise /token mints one.
		if h, ok := g.handlers[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		if r.URL.Path == "/token" {
			g.tokens++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, g.tokens)
			return
		}
		http.Error(w, `{"error":{"code":"itemNotFound"}}`, http.StatusNotFound)
	})

	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)

	// Point the driver at the fake for the duration of the test.
	oldBase, oldToken := graphBase, tokenURL
	graphBase = g.server.URL + "/v1.0"
	tokenURL = g.server.URL + "/token"
	t.Cleanup(func() { graphBase, tokenURL = oldBase, oldToken })

	return g
}

func (g *fakeGraph) handle(methodAndPath string, h http.HandlerFunc) {
	g.handlers[methodAndPath] = h
}

func (g *fakeGraph) json(methodAndPath string, status int, body string) {
	g.handle(methodAndPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// find returns the first recorded request matching method and path.
func (g *fakeGraph) find(method, path string) *recordedRequest {
	for i := range g.requests {
		if g.requests[i].Method == method && g.requests[i].Path == path {
			return &g.requests[i]
		}
	}
	return nil
}

func mountFake(t *testing.T, g *fakeGraph) *OneDriveDriver {
	t.Helper()
	d := &OneDriveDriver{}
	err := d.Mount(1, map[string]string{
		"client_id":     "cid",
		"client_secret": "secret",
		"refresh_token": "rtok",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })
	return d
}

func TestMountRefreshesToken(t *testing.T) {
	g := newFakeGraph(t)
	mountFake(t, g)

	if g.tokens != 1 {
		t.Errorf("token minted %d times, want 1", g.tokens)
	}
	req := g.find("POST", "/token")
	if req == nil {
		t.Fatal("no token request recorded")
	}
	for _, want := range []string{"client_id=cid", "refresh_token=rtok", "grant_type=refresh_token"} {
		if !strings.Contains(req.Body, want) {
			t.Errorf("token request body missing %q: %s", want, req.Body)
		}
	}
}

func TestMountFailsWhenTokenRefreshFails(t *testing.T) {
	g := newFakeGraph(t)
	g.json("POST /token", http.StatusUnauthorized, `{"error":"invalid_grant"}`)

	d := &OneDriveDriver{}
	err := d.Mount(1, map[string]string{"client_id": "cid", "refresh_token": "bad"})
	if err == nil {
		t.Fatal("mount succeeded with a rejected refresh token")
	}
	if !strings.Contains(err.Error(), "refresh") {
		t.Errorf("error does not mention the refresh: %v", err)
	}
}

func TestStatSendsBearerAndDecodesItem(t *testing.T) {
	g := newFakeGraph(t)
	g.json("GET /v1.0/me/drive/root:/notes.txt", http.StatusOK,
		`{"name":"notes.txt","size":42,"lastModifiedDateTime":"2026-01-02T03:04:05Z","file":{"mimeType":"text/plain"}}`)
	d := mountFake(t, g)

	fi, err := d.Stat(1, "/notes.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name != "notes.txt" || fi.Size != 42 || fi.IsDir {
		t.Errorf("unexpected file info: %+v", fi)
	}

	req := g.find("GET", "/v1.0/me/drive/root:/notes.txt")
	if req == nil {
		t.Fatal("no stat request recorded")
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-1" {
		t.Errorf("Authorization %q, want %q", got, "Bearer tok-1")
	}
}

func TestStatReportsDirectory(t *testing.T) {
	g := newFakeGraph(t)
	g.json("GET /v1.0/me/drive/root:/docs", http.StatusOK,
		`{"name":"docs","size":999,"folder":{"childCount":2}}`)
	d := mountFake(t, g)

	fi, err := d.Stat(1, "/docs")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir {
		t.Error("folder not reported as a directory")
	}
	if fi.Size != 0 {
		t.Errorf("directory size %d, want 0", fi.Size)
	}
}

// A listing arrives one page at a time, and the driver has to follow
// @odata.nextLink until it stops.
func TestListDirFollowsPagination(t *testing.T) {
	g := newFakeGraph(t)
	var pageTwo string
	g.handle("GET /v1.0/me/drive/root:/docs:/children", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"value":[{"name":"a.txt","size":1,"file":{}}],"@odata.nextLink":%q}`, pageTwo)
	})
	g.json("GET /v1.0/page2", http.StatusOK,
		`{"value":[{"name":"b.txt","size":2,"file":{}},{"name":"sub","folder":{}}]}`)
	pageTwo = "" // set after the server exists
	d := mountFake(t, g)
	pageTwo = g.server.URL + "/v1.0/page2"

	entries, err := d.ListDir(1, "/docs")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 across two pages: %+v", len(entries), entries)
	}
	if entries[0].Name != "a.txt" || entries[2].Name != "sub" {
		t.Errorf("unexpected order or contents: %+v", entries)
	}
	if !entries[2].IsDir {
		t.Error("sub not reported as a directory")
	}
	if entries[1].Path != "/docs/b.txt" {
		t.Errorf("child path %q, want /docs/b.txt", entries[1].Path)
	}
}

func TestOpenFileReturnsBody(t *testing.T) {
	g := newFakeGraph(t)
	g.json("GET /v1.0/me/drive/root:/a.txt:/content", http.StatusOK, "file contents")
	d := mountFake(t, g)

	rc, err := d.OpenFile(1, "/a.txt")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "file contents" {
		t.Errorf("read %q", body)
	}
}

func TestMkdirPostsFolderToParent(t *testing.T) {
	g := newFakeGraph(t)
	g.json("POST /v1.0/me/drive/root:/docs:/children", http.StatusCreated, `{"name":"new"}`)
	d := mountFake(t, g)

	if err := d.Mkdir(1, "/docs/new"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	req := g.find("POST", "/v1.0/me/drive/root:/docs:/children")
	if req == nil {
		t.Fatal("no mkdir request recorded")
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["name"] != "new" {
		t.Errorf("name %v, want new", body["name"])
	}
	if _, ok := body["folder"]; !ok {
		t.Error("body does not declare a folder")
	}
	if body["@microsoft.graph.conflictBehavior"] != "fail" {
		t.Errorf("conflictBehavior %v, want fail", body["@microsoft.graph.conflictBehavior"])
	}
}

func TestRemoveIssuesDelete(t *testing.T) {
	g := newFakeGraph(t)
	g.json("DELETE /v1.0/me/drive/root:/a.txt", http.StatusNoContent, "")
	d := mountFake(t, g)

	if err := d.Remove(1, "/a.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if g.find("DELETE", "/v1.0/me/drive/root:/a.txt") == nil {
		t.Error("no delete request recorded")
	}
}

// A rename within one folder changes the name; a move also carries the new
// parent. The single PATCH has to say the right thing in each case.
func TestRenamePatchesNameAndParent(t *testing.T) {
	for _, tt := range []struct {
		name       string
		from, to   string
		path       string
		wantName   string
		wantParent bool
	}{
		{"in place", "/docs/a.txt", "/docs/b.txt", "/v1.0/me/drive/root:/docs/a.txt", "b.txt", false},
		{"across folders", "/docs/a.txt", "/other/a.txt", "/v1.0/me/drive/root:/docs/a.txt", "a.txt", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newFakeGraph(t)
			g.json("PATCH "+tt.path, http.StatusOK, `{"name":"ok"}`)
			d := mountFake(t, g)

			if err := d.Rename(1, tt.from, tt.to); err != nil {
				t.Fatalf("Rename: %v", err)
			}
			req := g.find("PATCH", tt.path)
			if req == nil {
				t.Fatal("no patch request recorded")
			}
			var body map[string]interface{}
			if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			if body["name"] != tt.wantName {
				t.Errorf("name %v, want %v", body["name"], tt.wantName)
			}
			_, hasParent := body["parentReference"]
			if hasParent != tt.wantParent {
				t.Errorf("parentReference present = %v, want %v (body %s)", hasParent, tt.wantParent, req.Body)
			}
		})
	}
}

func TestCreateFileUploadsContent(t *testing.T) {
	g := newFakeGraph(t)
	g.json("PUT /v1.0/me/drive/root:/a.txt:/content", http.StatusCreated, `{"name":"a.txt","size":5}`)
	d := mountFake(t, g)

	w, err := d.CreateFile(1, "/a.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	req := g.find("PUT", "/v1.0/me/drive/root:/a.txt:/content")
	if req == nil {
		t.Fatal("no upload request recorded")
	}
	if req.Body != "hello" {
		t.Errorf("uploaded %q, want hello", req.Body)
	}
}

// A 401 mid-session means the token expired: the driver should mint a new one
// and retry rather than surfacing the failure.
func TestRetriesAfterUnauthorized(t *testing.T) {
	g := newFakeGraph(t)
	calls := 0
	g.handle("GET /v1.0/me/drive/root:/a.txt", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, `{"error":{"code":"unauthenticated"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"a.txt","size":1,"file":{}}`)
	})
	d := mountFake(t, g)

	fi, err := d.Stat(1, "/a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name != "a.txt" {
		t.Errorf("unexpected info %+v", fi)
	}
	if calls < 2 {
		t.Errorf("request attempted %d times, expected a retry", calls)
	}
	if g.tokens < 2 {
		t.Errorf("token minted %d times, expected a refresh after the 401", g.tokens)
	}
}

func TestStatOnMissingItemErrors(t *testing.T) {
	g := newFakeGraph(t)
	d := mountFake(t, g)

	if _, err := d.Stat(1, "/nope.txt"); err == nil {
		t.Error("Stat of a missing item succeeded")
	}
}

// Anything over uploadMemCap spills to a temp file and goes up through an
// upload session rather than a single PUT. The chunk goes to the URL the
// session hands back, which is Azure-backed rather than Graph, and so must
// carry no Authorization header — sending one there is a documented way to
// have the upload rejected.
func TestLargeUploadUsesSessionAndOmitsAuthOnChunks(t *testing.T) {
	g := newFakeGraph(t)

	var chunkAuth string
	var chunkRange string
	var received []byte
	g.handle("PUT /uploadurl", func(w http.ResponseWriter, r *http.Request) {
		chunkAuth = r.Header.Get("Authorization")
		chunkRange = r.Header.Get("Content-Range")
		b, _ := io.ReadAll(r.Body)
		received = append(received, b...)
		w.WriteHeader(http.StatusAccepted)
	})
	g.handle("POST /v1.0/me/drive/root:/big.bin:/createUploadSession",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"uploadUrl":%q}`, g.server.URL+"/uploadurl")
		})
	d := mountFake(t, g)

	payload := bytes.Repeat([]byte("x"), uploadMemCap+1024)

	w, err := d.CreateFile(1, "/big.bin")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if g.find("POST", "/v1.0/me/drive/root:/big.bin:/createUploadSession") == nil {
		t.Fatal("a large upload did not open an upload session")
	}
	if g.find("PUT", "/v1.0/me/drive/root:/big.bin:/content") != nil {
		t.Error("a large upload also used the small-file PUT")
	}
	if chunkAuth != "" {
		t.Errorf("chunk carried an Authorization header (%q); the upload URL is pre-authorised", chunkAuth)
	}
	if want := fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)); chunkRange != want {
		t.Errorf("Content-Range %q, want %q", chunkRange, want)
	}
	if len(received) != len(payload) {
		t.Errorf("server received %d bytes, want %d", len(received), len(payload))
	}
}

// A small file goes up in one PUT and never opens a session.
func TestSmallUploadUsesSinglePut(t *testing.T) {
	g := newFakeGraph(t)
	g.json("PUT /v1.0/me/drive/root:/small.txt:/content", http.StatusCreated, `{"name":"small.txt"}`)
	d := mountFake(t, g)

	w, _ := d.CreateFile(1, "/small.txt")
	if _, err := w.Write([]byte("tiny")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if g.find("POST", "/v1.0/me/drive/root:/small.txt:/createUploadSession") != nil {
		t.Error("a small upload opened an upload session")
	}
	if g.find("PUT", "/v1.0/me/drive/root:/small.txt:/content") == nil {
		t.Error("no single-PUT upload recorded")
	}
}

// A session that comes back without an uploadUrl is unusable and must be
// reported rather than followed.
func TestUploadSessionWithoutURLFails(t *testing.T) {
	g := newFakeGraph(t)
	g.json("POST /v1.0/me/drive/root:/big.bin:/createUploadSession", http.StatusOK, `{}`)
	d := mountFake(t, g)

	w, _ := d.CreateFile(1, "/big.bin")
	if _, err := w.Write(bytes.Repeat([]byte("y"), uploadMemCap+16)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err == nil {
		t.Error("close succeeded despite a session with no uploadUrl")
	}
}

func TestWriteAfterCloseFails(t *testing.T) {
	g := newFakeGraph(t)
	g.json("PUT /v1.0/me/drive/root:/a.txt:/content", http.StatusCreated, `{}`)
	d := mountFake(t, g)

	w, _ := d.CreateFile(1, "/a.txt")
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := w.Write([]byte("late")); err == nil {
		t.Error("write on a closed writer succeeded")
	}
}
