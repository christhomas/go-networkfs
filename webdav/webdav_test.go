package webdav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/net/webdav"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// --- unit tests -----------------------------------------------------------

func TestName(t *testing.T) {
	d := &WebDAVDriver{}
	if d.Name() != "webdav" {
		t.Fatalf("Name() = %q, want %q", d.Name(), "webdav")
	}
}

func TestDriverTypeID(t *testing.T) {
	if DriverTypeID != 5 {
		t.Fatalf("DriverTypeID = %d, want 5", DriverTypeID)
	}
}

func TestRegisteredInGlobalRegistry(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("driver type %d not registered", DriverTypeID)
	}
	if d.Name() != "webdav" {
		t.Fatalf("registry returned %q", d.Name())
	}
	if _, ok := d.(*WebDAVDriver); !ok {
		t.Fatalf("registry returned wrong concrete type: %T", d)
	}
}

func TestNotConnectedOperations(t *testing.T) {
	d := &WebDAVDriver{}
	if _, err := d.Stat(1, "/"); err == nil {
		t.Error("Stat should fail on unconnected driver")
	}
	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir should fail on unconnected driver")
	}
	if _, err := d.OpenFile(1, "/x"); err == nil {
		t.Error("OpenFile should fail on unconnected driver")
	}
	if _, err := d.CreateFile(1, "/x"); err == nil {
		t.Error("CreateFile should fail on unconnected driver")
	}
	if err := d.Mkdir(1, "/x"); err == nil {
		t.Error("Mkdir should fail on unconnected driver")
	}
	if err := d.Remove(1, "/x"); err == nil {
		t.Error("Remove should fail on unconnected driver")
	}
	if err := d.Rename(1, "/a", "/b"); err == nil {
		t.Error("Rename should fail on unconnected driver")
	}
}

func TestMountMissingHostAndURL(t *testing.T) {
	d := &WebDAVDriver{}
	if err := d.Mount(1, map[string]string{}); err == nil {
		t.Error("empty config should error (neither url nor host set)")
	}
	if err := d.Mount(1, nil); err == nil {
		t.Error("nil config should error")
	}
}

// --- integration tests against an embedded WebDAV server -------------------

// startWebDAVServer starts an httptest-backed WebDAV server using the
// golang.org/x/net/webdav filesystem over a temp directory. Returns the
// base URL and the on-disk directory so tests can assert against both.
func startWebDAVServer(tb testing.TB) (baseURL, rootDir string) {
	tb.Helper()
	rootDir = tb.TempDir()
	h := &webdav.Handler{
		FileSystem: webdav.Dir(rootDir),
		LockSystem: webdav.NewMemLS(),
	}
	srv := httptest.NewServer(h)
	tb.Cleanup(srv.Close)
	return srv.URL, rootDir
}

func mount(tb testing.TB, url string) *WebDAVDriver {
	tb.Helper()
	d := &WebDAVDriver{}
	if err := d.Mount(1, map[string]string{"url": url}); err != nil {
		tb.Fatalf("mount: %v", err)
	}
	tb.Cleanup(func() { _ = d.Unmount(1) })
	return d
}

func TestIntegrationListEmpty(t *testing.T) {
	url, _ := startWebDAVServer(t)
	d := mount(t, url)
	entries, err := d.ListDir(1, "/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dir, got %d", len(entries))
	}
}

func TestIntegrationCreateReadStatRemove(t *testing.T) {
	url, root := startWebDAVServer(t)
	d := mount(t, url)

	// Create a file via writer.
	w, err := d.CreateFile(1, "/hello.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "hello, webdav"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Confirm the file landed on disk where webdav.Dir put it.
	if _, err := os.Stat(filepath.Join(root, "hello.txt")); err != nil {
		t.Fatalf("backing file: %v", err)
	}

	// Stat correctly classifies the file.
	fi, err := d.Stat(1, "/hello.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.IsDir {
		t.Error("file reported as dir")
	}
	if fi.Size != int64(len("hello, webdav")) {
		t.Errorf("size = %d, want %d", fi.Size, len("hello, webdav"))
	}

	// ListDir sees it.
	entries, err := d.ListDir(1, "/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" {
		t.Fatalf("unexpected entries: %+v", entries)
	}

	// OpenFile reads the content.
	r, err := d.OpenFile(1, "/hello.txt")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello, webdav" {
		t.Fatalf("got %q", data)
	}

	// Remove it.
	if err := d.Remove(1, "/hello.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "hello.txt")); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestIntegrationMkdirAndRename(t *testing.T) {
	url, root := startWebDAVServer(t)
	d := mount(t, url)

	if err := d.Mkdir(1, "/sub"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	fi, err := os.Stat(filepath.Join(root, "sub"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("backing dir missing or not dir: %v", err)
	}

	w, err := d.CreateFile(1, "/sub/a.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "x"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := d.Rename(1, "/sub/a.txt", "/sub/b.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "b.txt")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	// Stat correctly classifies the directory as a dir.
	di, err := d.Stat(1, "/sub")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !di.IsDir {
		t.Error("directory reported as file")
	}
}

func TestIntegrationStatMissing(t *testing.T) {
	url, _ := startWebDAVServer(t)
	d := mount(t, url)
	if _, err := d.Stat(1, "/nope.txt"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestIntegrationMountBadURL(t *testing.T) {
	d := &WebDAVDriver{}
	err := d.Mount(1, map[string]string{"url": "http://127.0.0.1:1/notreal"})
	if err == nil {
		_ = d.Unmount(1)
		t.Fatal("expected connect error against unreachable url")
	}
}

func TestIntegrationStreamingWrite(t *testing.T) {
	url, _ := startWebDAVServer(t)
	d := mount(t, url)

	w, err := d.CreateFile(1, "/stream.bin")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	total := 64 * 1024
	for written := 0; written < total; written += len(chunk) {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("write at %d: %v", written, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	r, err := d.OpenFile(1, "/stream.bin")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != total {
		t.Fatalf("got %d bytes, want %d", len(got), total)
	}
}

// Guard that the handler type we depend on actually exists — silences
// "imported and not used" if the test file is ever trimmed.
var _ http.Handler = (*webdav.Handler)(nil)
