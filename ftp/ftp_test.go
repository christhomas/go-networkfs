package ftp

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/christhomas/go-networkfs/pkg/api"

	ftpserver "goftp.io/server/v2"
	"goftp.io/server/v2/driver/file"
)

// --- helper / pure-logic tests (no server needed) -------------------------

func TestIsConnError(t *testing.T) {
	d := &FTPDriver{}
	wantTrue := []error{
		errors.New("connection refused"),
		errors.New("use of closed network connection"),
		errors.New("something: EOF"),
		errors.New("broken pipe"),
		errors.New("connection reset by peer"),
	}
	for _, e := range wantTrue {
		if !d.isConnError(e) {
			t.Errorf("isConnError(%v) = false, want true", e)
		}
	}
	wantFalse := []error{
		nil,
		errors.New("permission denied"),
		errors.New("no such file"),
	}
	for _, e := range wantFalse {
		if d.isConnError(e) {
			t.Errorf("isConnError(%v) = true, want false", e)
		}
	}
}

func TestMountValidation(t *testing.T) {
	d := &FTPDriver{}
	// Missing host.
	if err := d.Mount(1, map[string]string{}); err == nil {
		t.Error("expected error when host is missing")
	}
	// Invalid port.
	err := d.Mount(1, map[string]string{"host": "x", "port": "notanum"})
	if err == nil {
		t.Error("expected error for invalid port")
	}
}

func TestNotConnectedOperations(t *testing.T) {
	d := &FTPDriver{}
	if _, err := d.Stat(1, "/"); err == nil {
		t.Error("Stat on unconnected driver should fail")
	}
	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir on unconnected driver should fail")
	}
	if _, err := d.OpenFile(1, "/x"); err == nil {
		t.Error("OpenFile on unconnected driver should fail")
	}
	if _, err := d.CreateFile(1, "/x"); err == nil {
		t.Error("CreateFile on unconnected driver should fail")
	}
	if err := d.Mkdir(1, "/x"); err == nil {
		t.Error("Mkdir on unconnected driver should fail")
	}
	if err := d.Remove(1, "/x"); err == nil {
		t.Error("Remove on unconnected driver should fail")
	}
	if err := d.Rename(1, "/a", "/b"); err == nil {
		t.Error("Rename on unconnected driver should fail")
	}
}

func TestName(t *testing.T) {
	if (&FTPDriver{}).Name() != "ftp" {
		t.Fatal("Name() should be ftp")
	}
}

func TestRegisteredInGlobalRegistry(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("driver type %d not registered", DriverTypeID)
	}
	if d.Name() != "ftp" {
		t.Fatalf("registry returned %q, want ftp", d.Name())
	}
}

// --- integration tests against an embedded FTP server --------------------

type testFTP struct {
	addr    string
	rootDir string
	srv     *ftpserver.Server
	lis     net.Listener
}

func startFTPServer(tb testing.TB) *testFTP {
	tb.Helper()
	dir := tb.TempDir()

	drv, err := file.NewDriver(dir)
	if err != nil {
		tb.Fatalf("file.NewDriver: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}

	srv, err := ftpserver.NewServer(&ftpserver.Options{
		Driver: drv,
		Perm:   ftpserver.NewSimplePerm("test", "test"),
		Auth: &ftpserver.SimpleAuth{
			Name:     "admin",
			Password: "admin",
		},
		Logger: new(ftpserver.DiscardLogger),
	})
	if err != nil {
		lis.Close()
		tb.Fatalf("NewServer: %v", err)
	}

	go func() { _ = srv.Serve(lis) }()

	// Close the listener directly rather than calling srv.Shutdown(),
	// which races with Serve's internal write of server.listener under
	// -race.
	tb.Cleanup(func() { _ = lis.Close() })

	ft := &testFTP{
		addr:    lis.Addr().String(),
		rootDir: dir,
		srv:     srv,
		lis:     lis,
	}
	waitForFTP(tb, ft.addr)
	return ft
}

func waitForFTP(tb testing.TB, addr string) {
	tb.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("ftp server never became ready at %s", addr)
}

func mount(tb testing.TB, ft *testFTP) *FTPDriver {
	tb.Helper()
	host, port, err := net.SplitHostPort(ft.addr)
	if err != nil {
		tb.Fatalf("split addr: %v", err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		tb.Fatalf("bad port %q", port)
	}
	d := &FTPDriver{}
	if err := d.Mount(7, map[string]string{
		"host": host,
		"port": port,
		"user": "admin",
		"pass": "admin",
		"root": "/",
	}); err != nil {
		tb.Fatalf("mount: %v", err)
	}
	tb.Cleanup(func() { _ = d.Unmount(7) })
	return d
}

func TestIntegrationListDirEmpty(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	entries, err := d.ListDir(7, "/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dir, got %d entries", len(entries))
	}
}

func TestIntegrationCreateListStatReadRemove(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	w, err := d.CreateFile(7, "/hello.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "hello world"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	if _, err := os.Stat(filepath.Join(ft.rootDir, "hello.txt")); err != nil {
		t.Fatalf("file not written to backing dir: %v", err)
	}

	entries, err := d.ListDir(7, "/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if entries[0].IsDir {
		t.Error("hello.txt should not be a dir")
	}
	if entries[0].Size != int64(len("hello world")) {
		t.Errorf("size = %d, want %d", entries[0].Size, len("hello world"))
	}

	// Stat must classify files correctly. The key regression my version
	// fixed was the old List-probe fallback marking every "unknown
	// entry" as a directory; upstream's MLST-with-LIST-parent fallback
	// should handle this correctly.
	info, err := d.Stat(7, "/hello.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.IsDir {
		t.Error("Stat: file reported as directory")
	}
	if info.Size != int64(len("hello world")) {
		t.Errorf("Stat size = %d, want %d", info.Size, len("hello world"))
	}

	r, err := d.OpenFile(7, "/hello.txt")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("read %q, want %q", data, "hello world")
	}

	if err := d.Remove(7, "/hello.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ft.rootDir, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still exists after Remove: %v", err)
	}
}

func TestIntegrationMkdirAndRename(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	if err := d.Mkdir(7, "/sub"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	info, err := os.Stat(filepath.Join(ft.rootDir, "sub"))
	if err != nil {
		t.Fatalf("backing dir missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("backing 'sub' is not a directory")
	}

	w, err := d.CreateFile(7, "/sub/a.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "x"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := d.Rename(7, "/sub/a.txt", "/sub/b.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ft.rootDir, "sub", "b.txt")); err != nil {
		t.Fatalf("rename target missing: %v", err)
	}

	entries, err := d.ListDir(7, "/sub")
	if err != nil {
		t.Fatalf("ListDir /sub: %v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "b.txt" {
		t.Fatalf("entries=%v, want [b.txt]", names)
	}

	if err := d.Remove(7, "/sub/b.txt"); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := d.Remove(7, "/sub"); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
}

func TestIntegrationStatDirectoryVsFile(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	if err := d.Mkdir(7, "/mixed"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	w, err := d.CreateFile(7, "/mixed/a.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	fi, err := d.Stat(7, "/mixed/a.txt")
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if fi.IsDir {
		t.Error("Stat: file reported as directory")
	}
	if fi.Size != 5 {
		t.Errorf("Stat: size = %d, want 5", fi.Size)
	}

	di, err := d.Stat(7, "/mixed")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !di.IsDir {
		t.Error("Stat: directory reported as file")
	}
}

func TestIntegrationStatRoot(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	info, err := d.Stat(7, "/")
	if err != nil {
		t.Fatalf("Stat /: %v", err)
	}
	if !info.IsDir {
		t.Error("Stat: root reported as file")
	}
}

func TestIntegrationStatMissing(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	if _, err := d.Stat(7, "/nope-not-here.txt"); err == nil {
		t.Fatal("expected error for missing path")
	}
}

// TestIntegrationStreamingWrite is specifically checking that
// CreateFile doesn't buffer the entire payload in memory before
// sending. My version used io.Pipe; a buffered-then-STOR implementation
// would still pass this test (same observable output) but exhibit the
// memory blow-up in practice. The important test is "does 256 KiB
// round-trip correctly" — memory-profile checks are out of scope here.
func TestIntegrationStreamingWrite(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	w, err := d.CreateFile(7, "/stream.bin")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}
	total := 256 * 1024
	for written := 0; written < total; written += len(chunk) {
		n, err := w.Write(chunk)
		if err != nil {
			t.Fatalf("Write at %d: %v", written, err)
		}
		if n != len(chunk) {
			t.Fatalf("short write %d/%d", n, len(chunk))
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := d.OpenFile(7, "/stream.bin")
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
	// Spot-check the repeating pattern.
	if !bytes.Equal(got[:len(chunk)], chunk) {
		t.Error("first chunk mismatch")
	}
}

func TestIntegrationReconnectAfterDrop(t *testing.T) {
	ft := startFTPServer(t)
	d := mount(t, ft)

	// Simulate a dropped server connection by quitting the underlying
	// ServerConn. The next op should surface a connection-class error
	// that withReconnect detects, reconnect the driver, and succeed.
	if err := d.client.Quit(); err != nil {
		t.Fatalf("force quit: %v", err)
	}

	if _, err := d.ListDir(7, "/"); err != nil {
		t.Fatalf("expected reconnect to succeed, got: %v", err)
	}
}

func TestIntegrationMountDefaultRootFillsSlash(t *testing.T) {
	ft := startFTPServer(t)
	host, port, _ := net.SplitHostPort(ft.addr)
	d := &FTPDriver{}
	if err := d.Mount(1, map[string]string{
		"host": host, "port": port, "user": "admin", "pass": "admin",
		// intentionally no "root"
	}); err != nil {
		t.Fatalf("mount: %v", err)
	}
	defer d.Unmount(1)
	if d.rootPath != "/" {
		t.Errorf("rootPath = %q, want /", d.rootPath)
	}
}

func TestIntegrationMountWrongPassword(t *testing.T) {
	ft := startFTPServer(t)
	host, port, _ := net.SplitHostPort(ft.addr)
	d := &FTPDriver{}
	err := d.Mount(1, map[string]string{
		"host": host,
		"port": port,
		"user": "admin",
		"pass": "wrong",
	})
	if err == nil {
		_ = d.Unmount(1)
		t.Fatal("expected mount failure with wrong password")
	}
}
