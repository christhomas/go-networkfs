//go:build smb_integration

// Integration tests for the SMB driver. Requires a reachable SMB server
// because no in-process Go SMB server exists.
//
// Build and run locally with:
//
//	SMB_HOST=192.168.1.10 SMB_SHARE=public SMB_USER=me SMB_PASS=secret \
//	  go test -tags=smb_integration -v -run Integration ./smb/...
//
// Optional env:
//
//	SMB_PORT   default 445
//	SMB_ROOT   default "" (share root). Tests create/remove files
//	           under this directory, so either point at a scratch share
//	           or set SMB_ROOT to a sandbox subdir the user owns.
//
// Without the smb_integration build tag this file is skipped entirely,
// so CI never needs an SMB server.

package smb

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func env(key string) string { return os.Getenv(key) }

func requireEnv(t *testing.T) map[string]string {
	t.Helper()
	cfg := map[string]string{
		"host":  env("SMB_HOST"),
		"share": env("SMB_SHARE"),
		"user":  env("SMB_USER"),
		"pass":  env("SMB_PASS"),
		"port":  env("SMB_PORT"),
		"root":  env("SMB_ROOT"),
	}
	missing := []string{}
	for _, k := range []string{"host", "share", "user", "pass"} {
		if cfg[k] == "" {
			missing = append(missing, strings.ToUpper("SMB_"+k))
		}
	}
	if len(missing) > 0 {
		t.Skipf("SMB integration skipped: set %s", strings.Join(missing, ", "))
	}
	return cfg
}

func mountIntegration(t *testing.T) *SMBDriver {
	t.Helper()
	cfg := requireEnv(t)
	d := &SMBDriver{}
	if err := d.Mount(1, cfg); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })
	return d
}

// uniquePath returns a test-scoped path prefix so concurrent runs don't
// collide.
func uniquePath(t *testing.T) string {
	return "/go-networkfs-smb-" + t.Name() + "-" + time.Now().Format("150405")
}

func TestIntegrationListRoot(t *testing.T) {
	d := mountIntegration(t)
	if _, err := d.ListDir(1, "/"); err != nil {
		t.Fatalf("ListDir: %v", err)
	}
}

func TestIntegrationCreateReadRemove(t *testing.T) {
	d := mountIntegration(t)
	path := uniquePath(t) + ".txt"

	w, err := d.CreateFile(1, path)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "hello, smb"); err != nil {
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
	if fi.Size != int64(len("hello, smb")) {
		t.Errorf("size = %d", fi.Size)
	}

	r, err := d.OpenFile(1, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello, smb" {
		t.Fatalf("got %q", got)
	}

	if err := d.Remove(1, path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := d.Stat(1, path); err == nil {
		t.Fatal("file should be gone")
	}
}

func TestIntegrationMkdirRename(t *testing.T) {
	d := mountIntegration(t)
	dir := uniquePath(t) + "-dir"

	if err := d.Mkdir(1, dir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(1, dir) })
	t.Cleanup(func() { _ = d.Remove(1, dir+"/b.txt") })

	w, _ := d.CreateFile(1, dir+"/a.txt")
	if _, err := io.WriteString(w, "x"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := d.Rename(1, dir+"/a.txt", dir+"/b.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := d.Stat(1, dir+"/b.txt"); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	fi, err := d.Stat(1, dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !fi.IsDir {
		t.Error("directory classified as file")
	}
}

func TestIntegrationWrongPassword(t *testing.T) {
	cfg := requireEnv(t)
	cfg["pass"] = "definitely-not-the-right-password"
	d := &SMBDriver{}
	if err := d.Mount(1, cfg); err == nil {
		_ = d.Unmount(1)
		t.Fatal("wrong password should fail")
	}
}
