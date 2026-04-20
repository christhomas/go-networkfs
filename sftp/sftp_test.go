package sftp

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/christhomas/go-networkfs/pkg/api"
	pkgsftp "github.com/pkg/sftp"
)

// --- unit tests ----------------------------------------------------------

func TestName(t *testing.T) {
	d := &SFTPDriver{}
	if got := d.Name(); got != "sftp" {
		t.Fatalf("Name() = %q, want %q", got, "sftp")
	}
}

func TestRegisteredInGlobalRegistry(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("driver type %d not registered", DriverTypeID)
	}
	if d.Name() != "sftp" {
		t.Fatalf("registry returned %q, want %q", d.Name(), "sftp")
	}
	if _, ok := d.(*SFTPDriver); !ok {
		t.Fatalf("registry returned unexpected concrete type %T", d)
	}
}

func TestNotConnectedOperations(t *testing.T) {
	d := &SFTPDriver{}
	if _, err := d.Stat(1, "/"); err != api.ErrNotConnected {
		t.Errorf("Stat: got %v, want ErrNotConnected", err)
	}
	if _, err := d.ListDir(1, "/"); err != api.ErrNotConnected {
		t.Errorf("ListDir: got %v, want ErrNotConnected", err)
	}
	if _, err := d.OpenFile(1, "/x"); err != api.ErrNotConnected {
		t.Errorf("OpenFile: got %v, want ErrNotConnected", err)
	}
	if _, err := d.CreateFile(1, "/x"); err != api.ErrNotConnected {
		t.Errorf("CreateFile: got %v, want ErrNotConnected", err)
	}
	if err := d.Mkdir(1, "/x"); err != api.ErrNotConnected {
		t.Errorf("Mkdir: got %v, want ErrNotConnected", err)
	}
	if err := d.Remove(1, "/x"); err != api.ErrNotConnected {
		t.Errorf("Remove: got %v, want ErrNotConnected", err)
	}
	if err := d.Rename(1, "/a", "/b"); err != api.ErrNotConnected {
		t.Errorf("Rename: got %v, want ErrNotConnected", err)
	}
}

func TestMountValidationMissingHost(t *testing.T) {
	d := &SFTPDriver{}
	if err := d.Mount(1, map[string]string{}); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestMountValidationInvalidPort(t *testing.T) {
	d := &SFTPDriver{}
	err := d.Mount(1, map[string]string{
		"host": "127.0.0.1",
		"user": "x",
		"port": "not-a-number",
		"pass": "x",
	})
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestMountValidationMissingUser(t *testing.T) {
	// Upstream validates user at connect() time, not Mount()-arg parse.
	// With no user and no reachable server it must still error.
	d := &SFTPDriver{}
	err := d.Mount(1, map[string]string{
		"host": "127.0.0.1",
		"port": "1", // unlikely to be listening
		"pass": "x",
	})
	if err == nil {
		_ = d.Unmount(1)
		t.Fatal("expected error for missing user")
	}
}

// --- integration tests against an embedded SSH + SFTP server -------------

type testSFTP struct {
	addr    string
	rootDir string
}

func startSFTPServer(tb testing.TB) *testSFTP {
	tb.Helper()
	dir := tb.TempDir()

	hostKey := generateHostKey(tb)

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "test" && string(pass) == "test" {
				return nil, nil
			}
			return nil, fmt.Errorf("bad creds")
		},
	}
	cfg.AddHostKey(hostKey)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go serveSFTP(tb, conn, cfg)
		}
	}()

	tb.Cleanup(func() {
		_ = lis.Close()
		<-done
	})

	return &testSFTP{
		addr:    lis.Addr().String(),
		rootDir: dir,
	}
}

func serveSFTP(tb testing.TB, nConn net.Conn, cfg *ssh.ServerConfig) {
	defer nConn.Close()
	_, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			return
		}
		go func(in <-chan *ssh.Request) {
			for req := range in {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				_ = req.Reply(ok, nil)
			}
		}(requests)

		server, err := pkgsftp.NewServer(ch)
		if err != nil {
			return
		}
		if err := server.Serve(); err != nil && err != io.EOF {
			tb.Logf("sftp server exit: %v", err)
		}
		_ = server.Close()
	}
}

func generateHostKey(tb testing.TB) ssh.Signer {
	tb.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatalf("rsa: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(k)
	if err != nil {
		tb.Fatalf("signer: %v", err)
	}
	return signer
}

func mount(tb testing.TB, srv *testSFTP) *SFTPDriver {
	tb.Helper()
	host, port, err := net.SplitHostPort(srv.addr)
	if err != nil {
		tb.Fatalf("split: %v", err)
	}
	d := &SFTPDriver{}
	cfg := map[string]string{
		"host": host,
		"port": port,
		"user": "test",
		"pass": "test",
		"root": srv.rootDir,
	}
	if err := d.Mount(1, cfg); err != nil {
		tb.Fatalf("mount: %v", err)
	}
	tb.Cleanup(func() { _ = d.Unmount(1) })
	return d
}

func TestIntegrationListEmpty(t *testing.T) {
	srv := startSFTPServer(t)
	d := mount(t, srv)
	entries, err := d.ListDir(1, "/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dir, got %d entries", len(entries))
	}
}

func TestIntegrationCreateReadStatRemove(t *testing.T) {
	srv := startSFTPServer(t)
	d := mount(t, srv)

	w, err := d.CreateFile(1, "/hello.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if _, err := io.WriteString(w, "hello, sftp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(srv.rootDir, "hello.txt")); err != nil {
		t.Fatalf("backing file: %v", err)
	}

	fi, err := d.Stat(1, "/hello.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.IsDir {
		t.Error("file reported as dir")
	}
	if fi.Size != int64(len("hello, sftp")) {
		t.Errorf("size = %d", fi.Size)
	}

	r, err := d.OpenFile(1, "/hello.txt")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello, sftp" {
		t.Fatalf("got %q", data)
	}

	if err := d.Remove(1, "/hello.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.rootDir, "hello.txt")); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestIntegrationMkdirRenameStatDir(t *testing.T) {
	srv := startSFTPServer(t)
	d := mount(t, srv)

	if err := d.Mkdir(1, "/sub"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	fi, err := os.Stat(filepath.Join(srv.rootDir, "sub"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("backing dir: %v", err)
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
	if _, err := os.Stat(filepath.Join(srv.rootDir, "sub", "b.txt")); err != nil {
		t.Fatalf("renamed missing: %v", err)
	}

	di, err := d.Stat(1, "/sub")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !di.IsDir {
		t.Error("directory classified as file")
	}

	// And a file Stat for contrast.
	fiDrv, err := d.Stat(1, "/sub/b.txt")
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if fiDrv.IsDir {
		t.Error("file classified as dir")
	}

	if err := d.Remove(1, "/sub/b.txt"); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := d.Remove(1, "/sub"); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
}

func TestIntegrationStatMissing(t *testing.T) {
	srv := startSFTPServer(t)
	d := mount(t, srv)
	if _, err := d.Stat(1, "/nope-not-here.txt"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestIntegrationWrongPassword(t *testing.T) {
	srv := startSFTPServer(t)
	host, port, _ := net.SplitHostPort(srv.addr)
	d := &SFTPDriver{}
	err := d.Mount(1, map[string]string{
		"host": host,
		"port": port,
		"user": "test",
		"pass": "wrong",
	})
	if err == nil {
		_ = d.Unmount(1)
		t.Fatal("wrong password should fail")
	}
}

func TestIntegrationStreamingWrite(t *testing.T) {
	srv := startSFTPServer(t)
	d := mount(t, srv)

	w, err := d.CreateFile(1, "/stream.bin")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	total := 128 * 1024
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
