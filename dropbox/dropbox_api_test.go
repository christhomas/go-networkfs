package dropbox

// Tests driven by a stand-in for the Dropbox API.
//
// Every operation this driver performs is an SDK call over HTTP, so none of it
// runs without something at the other end. What the tests covered before was
// dbxPath and the error wrapper.
//
// The SDK builds its URLs as https://<host>.dropbox.com, with the host varying
// by route, and exposes a URLGenerator hook for exactly this. The driver takes
// an api_base_url config key that installs one, so the substitute below is
// reached the same way a mock in a container would be.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// entry is one file or folder in the mock's memory.
type entry struct {
	name   string
	isDir  bool
	body   []byte
	rev    string
	server string
}

// fakeDropbox serves the seven routes this driver uses, backed by a map keyed
// on the Dropbox path ("" is the root).
type fakeDropbox struct {
	t       *testing.T
	server  *httptest.Server
	files   map[string]*entry
	calls   []string
	failAll string // when set, every route answers with this error tag
}

func newFakeDropbox(t *testing.T) *fakeDropbox {
	t.Helper()
	f := &fakeDropbox{t: t, files: map[string]*entry{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)

		if f.failAll != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"error_summary":%q,"error":{".tag":%q}}`, f.failAll, f.failAll)
			return
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "/files/get_metadata"):
			f.getMetadata(w, r)
		case strings.HasSuffix(r.URL.Path, "/files/list_folder"):
			f.listFolder(w, r)
		case strings.HasSuffix(r.URL.Path, "/files/create_folder_v2"):
			f.createFolder(w, r)
		case strings.HasSuffix(r.URL.Path, "/files/delete_v2"):
			f.deleteEntry(w, r)
		case strings.HasSuffix(r.URL.Path, "/files/move_v2"):
			f.move(w, r)
		case strings.HasSuffix(r.URL.Path, "/files/download"):
			f.download(w, r)
		case strings.HasSuffix(r.URL.Path, "/files/upload"):
			f.upload(w, r)
		default:
			http.Error(w, `{"error_summary":"unknown_route"}`, http.StatusNotFound)
		}
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// arg reads the request argument, which is a JSON body on API routes and the
// Dropbox-API-Arg header on content routes.
func (f *fakeDropbox) arg(r *http.Request) map[string]interface{} {
	var raw []byte
	if h := r.Header.Get("Dropbox-API-Arg"); h != "" {
		raw = []byte(h)
	} else {
		raw, _ = io.ReadAll(r.Body)
	}
	out := map[string]interface{}{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (f *fakeDropbox) argPath(r *http.Request) string {
	p, _ := f.arg(r)["path"].(string)
	return p
}

func metaJSON(path string, e *entry) string {
	tag := "file"
	if e.isDir {
		tag = "folder"
	}
	name := e.name
	if name == "" {
		name = "root"
	}
	if e.isDir {
		return fmt.Sprintf(`{".tag":%q,"name":%q,"path_lower":%q,"path_display":%q,"id":"id:%s"}`,
			tag, name, strings.ToLower(path), path, name)
	}
	return fmt.Sprintf(
		`{".tag":%q,"name":%q,"path_lower":%q,"path_display":%q,"id":"id:%s",`+
			`"size":%d,"rev":"a1","client_modified":"2026-01-02T03:04:05Z",`+
			`"server_modified":"2026-01-02T03:04:05Z"}`,
		tag, name, strings.ToLower(path), path, name, len(e.body))
}

func (f *fakeDropbox) notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(w,
		`{"error_summary":"path/not_found/","error":{".tag":"path","path":{".tag":"not_found"}}}`)
}

func (f *fakeDropbox) getMetadata(w http.ResponseWriter, r *http.Request) {
	p := f.argPath(r)
	e, ok := f.files[p]
	if !ok {
		f.notFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, metaJSON(p, e))
}

func (f *fakeDropbox) listFolder(w http.ResponseWriter, r *http.Request) {
	parent := f.argPath(r)

	var parts []string
	for p, e := range f.files {
		if p == parent || !strings.HasPrefix(p, parent+"/") {
			continue
		}
		// Direct children only.
		if strings.Contains(strings.TrimPrefix(p, parent+"/"), "/") {
			continue
		}
		parts = append(parts, metaJSON(p, e))
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"entries":[%s],"cursor":"c1","has_more":false}`, strings.Join(parts, ","))
}

func (f *fakeDropbox) createFolder(w http.ResponseWriter, r *http.Request) {
	p := f.argPath(r)
	name := p[strings.LastIndex(p, "/")+1:]
	f.files[p] = &entry{name: name, isDir: true}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"metadata":%s}`, metaJSON(p, f.files[p]))
}

func (f *fakeDropbox) deleteEntry(w http.ResponseWriter, r *http.Request) {
	p := f.argPath(r)
	e, ok := f.files[p]
	if !ok {
		f.notFound(w)
		return
	}
	delete(f.files, p)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"metadata":%s}`, metaJSON(p, e))
}

func (f *fakeDropbox) move(w http.ResponseWriter, r *http.Request) {
	a := f.arg(r)
	from, _ := a["from_path"].(string)
	to, _ := a["to_path"].(string)

	e, ok := f.files[from]
	if !ok {
		f.notFound(w)
		return
	}
	delete(f.files, from)
	e.name = to[strings.LastIndex(to, "/")+1:]
	f.files[to] = e

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"metadata":%s}`, metaJSON(to, e))
}

func (f *fakeDropbox) download(w http.ResponseWriter, r *http.Request) {
	p := f.argPath(r)
	e, ok := f.files[p]
	if !ok || e.isDir {
		f.notFound(w)
		return
	}
	w.Header().Set("Dropbox-API-Result", metaJSON(p, e))
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(e.body)
}

func (f *fakeDropbox) upload(w http.ResponseWriter, r *http.Request) {
	p := f.argPath(r)
	body, _ := io.ReadAll(r.Body)
	name := p[strings.LastIndex(p, "/")+1:]
	f.files[p] = &entry{name: name, body: body}

	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, metaJSON(p, f.files[p]))
}

func mountFake(t *testing.T, f *fakeDropbox) *DropboxDriver {
	t.Helper()
	d := &DropboxDriver{}
	err := d.Mount(1, map[string]string{
		"access_token": "test-token",
		"api_base_url": f.server.URL,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })
	return d
}

func TestMountRequiresToken(t *testing.T) {
	d := &DropboxDriver{}
	if err := d.Mount(1, map[string]string{}); err == nil {
		t.Error("mount succeeded without an access token")
	}
}

func TestStatFile(t *testing.T) {
	f := newFakeDropbox(t)
	f.files["/a.txt"] = &entry{name: "a.txt", body: []byte("hello")}
	d := mountFake(t, f)

	fi, err := d.Stat(1, "/a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name != "a.txt" || fi.IsDir {
		t.Errorf("unexpected info %+v", fi)
	}
	if fi.Size != 5 {
		t.Errorf("size %d, want 5", fi.Size)
	}
}

func TestStatFolder(t *testing.T) {
	f := newFakeDropbox(t)
	f.files["/docs"] = &entry{name: "docs", isDir: true}
	d := mountFake(t, f)

	fi, err := d.Stat(1, "/docs")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir {
		t.Error("folder not reported as a directory")
	}
}

func TestStatMissing(t *testing.T) {
	f := newFakeDropbox(t)
	d := mountFake(t, f)

	if _, err := d.Stat(1, "/nope.txt"); err == nil {
		t.Error("Stat of a missing path succeeded")
	}
}

// The root is "" to Dropbox, not "/", and getting that wrong is a request for
// a path that cannot exist.
func TestListDirRootUsesEmptyPath(t *testing.T) {
	f := newFakeDropbox(t)
	f.files["/a.txt"] = &entry{name: "a.txt", body: []byte("x")}
	f.files["/dir"] = &entry{name: "dir", isDir: true}
	f.files["/dir/nested.txt"] = &entry{name: "nested.txt", body: []byte("y")}
	d := mountFake(t, f)

	entries, err := d.ListDir(1, "/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries at the root, want 2 (nested must not appear): %+v", len(entries), entries)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name] = e.IsDir
	}
	if isDir, ok := seen["dir"]; !ok || !isDir {
		t.Errorf("dir missing or not a directory: %v", seen)
	}
}

func TestOpenFileReadsContent(t *testing.T) {
	f := newFakeDropbox(t)
	f.files["/a.txt"] = &entry{name: "a.txt", body: []byte("file contents")}
	d := mountFake(t, f)

	rc, err := d.OpenFile(1, "/a.txt")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "file contents" {
		t.Errorf("read %q", got)
	}
}

// Writes are buffered and only sent on Close, so nothing should reach the
// server until then.
func TestCreateFileUploadsOnClose(t *testing.T) {
	f := newFakeDropbox(t)
	d := mountFake(t, f)

	w, err := d.CreateFile(1, "/new.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := w.Write([]byte("written")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := f.files["/new.txt"]; ok {
		t.Error("the file was uploaded before Close")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	e, ok := f.files["/new.txt"]
	if !ok {
		t.Fatal("nothing uploaded on Close")
	}
	if string(e.body) != "written" {
		t.Errorf("uploaded %q, want written", e.body)
	}
}

func TestCloseTwiceIsSafe(t *testing.T) {
	f := newFakeDropbox(t)
	d := mountFake(t, f)

	w, _ := d.CreateFile(1, "/x.txt")
	if err := w.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second close returned %v, want nil", err)
	}
}

func TestMkdirRemoveRename(t *testing.T) {
	f := newFakeDropbox(t)
	d := mountFake(t, f)

	if err := d.Mkdir(1, "/newdir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if e, ok := f.files["/newdir"]; !ok || !e.isDir {
		t.Fatal("Mkdir did not create a folder")
	}

	if err := d.Rename(1, "/newdir", "/moved"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, ok := f.files["/newdir"]; ok {
		t.Error("the source survived the rename")
	}
	if _, ok := f.files["/moved"]; !ok {
		t.Error("the destination does not exist after the rename")
	}

	if err := d.Remove(1, "/moved"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := f.files["/moved"]; ok {
		t.Error("Remove left the entry behind")
	}
}

// A missing scope is the failure an operator is most likely to hit and least
// likely to understand, so the driver adds a hint. That hint is the thing
// worth pinning.
func TestMissingScopeErrorIsExplained(t *testing.T) {
	f := newFakeDropbox(t)
	f.failAll = "missing_scope/..."
	d := mountFake(t, f)

	_, err := d.Stat(1, "/a.txt")
	if err == nil {
		t.Fatal("Stat succeeded despite a missing scope")
	}
	if !strings.Contains(err.Error(), "permission scope") {
		t.Errorf("error %q does not explain the missing scope", err)
	}
}

func TestOperationsBeforeMount(t *testing.T) {
	d := &DropboxDriver{}
	if _, err := d.Stat(1, "/a"); err == nil {
		t.Error("Stat succeeded before Mount")
	}
	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir succeeded before Mount")
	}
	if _, err := d.CreateFile(1, "/a"); err == nil {
		t.Error("CreateFile succeeded before Mount")
	}
	if err := d.Mkdir(1, "/a"); err == nil {
		t.Error("Mkdir succeeded before Mount")
	}
}
