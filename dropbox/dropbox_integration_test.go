//go:build dropbox_integration

// Integration tests for the Dropbox driver. Requires a real Dropbox
// account because upstream's driver uses the official SDK and offers no
// seam to redirect requests to a mock server.
//
// Build and run locally with:
//
//	DROPBOX_TOKEN=sl.xxx... \
//	  go test -tags=dropbox_integration -v -run Integration ./dropbox/...
//
// Optional env:
//
//	DROPBOX_TEST_ROOT   default "/go-networkfs-test" — a sandbox folder
//	                    the token owns; tests create/remove entries
//	                    under it. Leaves no residue on success.
//
// Without the dropbox_integration build tag this file is excluded from
// the build, so CI never needs a Dropbox token.

package dropbox

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// mountOrSkip builds a live-Dropbox driver or skips the test. Ensures
// the sandbox root directory exists up front.
func mountOrSkip(t *testing.T) (*DropboxDriver, string) {
	t.Helper()
	token := os.Getenv("DROPBOX_TOKEN")
	if token == "" {
		t.Skip("DROPBOX_TOKEN not set; skipping live Dropbox integration test")
	}
	root := os.Getenv("DROPBOX_TEST_ROOT")
	if root == "" {
		root = "/go-networkfs-test"
	}
	if !strings.HasPrefix(root, "/") {
		root = "/" + root
	}

	d := &DropboxDriver{}
	if err := d.Mount(1, map[string]string{"access_token": token}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })

	// Ensure sandbox root exists; ignore errors (likely "already exists").
	_ = d.Mkdir(1, root)
	return d, root
}

// uniquePath returns a per-test path under root so parallel or repeated
// runs don't collide.
func uniquePath(root, name string) string {
	return root + "/" + time.Now().UTC().Format("20060102T150405.000000000") + "-" + name
}

func TestIntegrationStatRoot(t *testing.T) {
	d, root := mountOrSkip(t)
	fi, err := d.Stat(1, root)
	if err != nil {
		t.Fatalf("Stat %q: %v", root, err)
	}
	if !fi.IsDir {
		t.Errorf("%q not classified as dir", root)
	}
}

func TestIntegrationUploadStatDownloadRemove(t *testing.T) {
	d, root := mountOrSkip(t)
	path := uniquePath(root, "hello.txt")

	w, err := d.CreateFile(1, path)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	payload := "hello, dropbox"
	if _, err := io.WriteString(w, payload); err != nil {
		_ = w.Close()
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(1, path) })

	fi, err := d.Stat(1, path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.IsDir {
		t.Error("file reported as dir")
	}
	if fi.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", fi.Size, len(payload))
	}

	r, err := d.OpenFile(1, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("got %q, want %q", got, payload)
	}

	if err := d.Remove(1, path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := d.Stat(1, path); err == nil {
		t.Fatal("file should be gone after Remove")
	}
}

func TestIntegrationMkdirListRename(t *testing.T) {
	d, root := mountOrSkip(t)
	sub := uniquePath(root, "sub")
	t.Cleanup(func() { _ = d.Remove(1, sub) })

	if err := d.Mkdir(1, sub); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	filePath := sub + "/a.txt"
	w, err := d.CreateFile(1, filePath)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "x"); err != nil {
		_ = w.Close()
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := d.ListDir(1, sub)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("entries = %+v", entries)
	}

	newPath := sub + "/b.txt"
	if err := d.Rename(1, filePath, newPath); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := d.Stat(1, filePath); err == nil {
		t.Fatal("source should be gone after Rename")
	}
	if _, err := d.Stat(1, newPath); err != nil {
		t.Fatalf("target missing after Rename: %v", err)
	}
}

func TestIntegrationLargeUploadRoundTrip(t *testing.T) {
	d, root := mountOrSkip(t)
	path := uniquePath(root, "big.bin")
	t.Cleanup(func() { _ = d.Remove(1, path) })

	w, err := d.CreateFile(1, path)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	payload := make([]byte, 128*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	if _, err := w.Write(payload); err != nil {
		_ = w.Close()
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	r, err := d.OpenFile(1, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("got %d bytes, want %d", len(got), len(payload))
	}
	for i := range got {
		if got[i] != payload[i] {
			t.Fatalf("byte %d mismatch: got %d, want %d", i, got[i], payload[i])
		}
	}
}
