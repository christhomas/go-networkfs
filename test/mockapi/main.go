// A stand-in for the three hosted APIs this project talks to.
//
// Dropbox, Google Drive and OneDrive cannot be run locally the way Samba or
// MinIO can, so the drivers that use them could only ever be tested against
// in-process fakes. That leaves the C ABI harnesses out: they are separate
// programs and cannot reach an httptest server inside a Go test.
//
// This is the same idea as those fakes, as a standalone server. It speaks
// enough of each API for the drivers to mount and do real work, backed by one
// in-memory filesystem shared by all three, so the same file written through
// the Dropbox routes can be read back through the Drive ones.
//
// It is a test double, not an emulator. Anything the drivers do not call is
// absent, and it holds no data across restarts.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// node is one file or directory. Directories carry no bytes.
type node struct {
	name  string
	dir   bool
	body  []byte
	id    string
	mtime time.Time
}

// store is the filesystem all three APIs share. Paths are absolute and
// slash-separated; the root is "/".
type store struct {
	mu     sync.Mutex
	nodes  map[string]*node
	nextID int
}

func newStore() *store {
	s := &store{nodes: map[string]*node{}}
	s.nodes["/"] = &node{name: "", dir: true, id: "root", mtime: time.Now()}
	return s
}

func (s *store) get(path string) (*node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[normalise(path)]
	return n, ok
}

func (s *store) byID(id string) (string, *node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, n := range s.nodes {
		if n.id == id {
			return p, n, true
		}
	}
	return "", nil, false
}

func (s *store) put(path string, dir bool, body []byte) *node {
	s.mu.Lock()
	defer s.mu.Unlock()

	path = normalise(path)
	if existing, ok := s.nodes[path]; ok {
		existing.body = body
		existing.mtime = time.Now()
		return existing
	}
	s.nextID++
	n := &node{
		name:  base(path),
		dir:   dir,
		body:  body,
		id:    fmt.Sprintf("id-%d", s.nextID),
		mtime: time.Now(),
	}
	s.nodes[path] = n
	return n
}

func (s *store) remove(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	path = normalise(path)
	if _, ok := s.nodes[path]; !ok {
		return false
	}
	// A directory takes its contents with it.
	for p := range s.nodes {
		if p == path || strings.HasPrefix(p, path+"/") {
			delete(s.nodes, p)
		}
	}
	return true
}

func (s *store) move(from, to string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	from, to = normalise(from), normalise(to)
	n, ok := s.nodes[from]
	if !ok {
		return false
	}
	delete(s.nodes, from)
	n.name = base(to)
	s.nodes[to] = n
	return true
}

// children returns the direct children of a directory, in name order so
// listings are stable.
func (s *store) children(path string) []struct {
	Path string
	Node *node
} {
	s.mu.Lock()
	defer s.mu.Unlock()

	path = normalise(path)
	prefix := path
	if prefix != "/" {
		prefix += "/"
	}

	var out []struct {
		Path string
		Node *node
	}
	for p, n := range s.nodes {
		if p == path || !strings.HasPrefix(p, prefix) {
			continue
		}
		if strings.Contains(strings.TrimPrefix(p, prefix), "/") {
			continue
		}
		out = append(out, struct {
			Path string
			Node *node
		}{p, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func normalise(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		return "/"
	}
	return p
}

func base(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func main() {
	addr := os.Getenv("MOCKAPI_ADDR")
	if addr == "" {
		addr = ":8081"
	}

	s := newStore()
	mux := http.NewServeMux()

	// Token endpoints. Each driver is pointed at its own prefix, so each gets
	// its own, and any credentials are accepted: what is under test is the
	// driver's handling of the exchange, not the exchange itself.
	token := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK,
			`{"access_token":"mock-token","expires_in":3600,"token_type":"Bearer"}`)
	}
	tokenInfo := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"expires_in":3600}`)
	}
	for _, prefix := range []string{"", "/gdrive", "/onedrive", "/dropbox"} {
		mux.HandleFunc(prefix+"/token", token)
		mux.HandleFunc(prefix+"/oauth2/v1/tokeninfo", tokenInfo)
	}

	registerDropbox(mux, s)
	registerDrive(mux, s)
	registerGraph(mux, s)
	registerUpload(mux, s)

	log.Printf("mock api listening on %s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// jsonArg decodes a request argument, which Dropbox sends as a JSON body on
// API routes and as a header on content routes.
func jsonArg(r *http.Request) map[string]any {
	var raw []byte
	if h := r.Header.Get("Dropbox-API-Arg"); h != "" {
		raw = []byte(h)
	} else {
		raw, _ = io.ReadAll(r.Body)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func argString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// decodeJSON is a thin wrapper so the route files do not each import
// encoding/json for one call.
func decodeJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// registerUpload serves the chunked upload URL a Graph upload session hands
// back. Chunks arrive with a Content-Range, and the last one completes the
// file.
func registerUpload(mux *http.ServeMux, s *store) {
	var mu sync.Mutex
	partial := map[string][]byte{}

	mux.HandleFunc("/onedrive/upload", func(w http.ResponseWriter, r *http.Request) {
		path := normalise(r.URL.Query().Get("path"))
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		partial[path] = append(partial[path], body...)
		total := partial[path]
		mu.Unlock()

		// "bytes a-b/total": the upload is finished when b+1 == total.
		done := true
		if cr := r.Header.Get("Content-Range"); cr != "" {
			var first, last, size int
			if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &first, &last, &size); err == nil {
				done = last+1 >= size
			}
		}

		if !done {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		mu.Lock()
		delete(partial, path)
		mu.Unlock()

		n := s.put(path, false, total)
		writeJSON(w, http.StatusCreated, graphMeta(n))
	})
}
