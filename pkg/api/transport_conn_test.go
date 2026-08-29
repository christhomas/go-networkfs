// pkg/api/transport_conn_test.go - Unit tests for CountingConn.
//
// These tests use net.Pipe so we don't need to bind any sockets;
// the in-memory pipe satisfies net.Conn and lets us drive both ends
// from a single goroutine pair.

package api

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// TestCountingConnRead asserts BytesRead and OpsRead increment on
// every Read regardless of size, and that the byte count matches the
// number of bytes the inner conn returned.
func TestCountingConnRead(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	stats := &MountStats{}
	wrapped := WrapConn(client, stats)

	// Push 13 bytes from the server side.
	go func() {
		_, _ = server.Write([]byte("hello, world!"))
	}()

	buf := make([]byte, 1024)
	n, err := wrapped.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read: %v", err)
	}
	if n != 13 {
		t.Fatalf("Read returned %d bytes, want 13", n)
	}

	br, _, opsR, _ := stats.Snapshot()
	if br != 13 {
		t.Errorf("BytesRead = %d, want 13", br)
	}
	if opsR != 1 {
		t.Errorf("OpsRead = %d, want 1", opsR)
	}
}

// TestCountingConnWrite is the symmetric Write check.
func TestCountingConnWrite(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	stats := &MountStats{}
	wrapped := WrapConn(client, stats)

	// Drain the server side so Write doesn't block forever.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.ReadAll(server)
	}()

	payload := []byte("hello, write!")
	n, err := wrapped.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d bytes, want %d", n, len(payload))
	}

	_ = wrapped.Close()
	<-done

	_, bw, _, opsW := stats.Snapshot()
	if bw != uint64(len(payload)) {
		t.Errorf("BytesWritten = %d, want %d", bw, len(payload))
	}
	if opsW != 1 {
		t.Errorf("OpsWritten = %d, want 1", opsW)
	}
}

// TestCountingConnMultipleOps verifies counters accumulate across
// multiple read/write calls and that each call bumps Ops by exactly one
// regardless of the byte count.
func TestCountingConnMultipleOps(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	stats := &MountStats{}
	wrapped := WrapConn(client, stats)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Echo: read into a small buffer so the client's writes are
		// drained one at a time, then write a known reply per round.
		buf := make([]byte, 4)
		for i := 0; i < 3; i++ {
			if _, err := io.ReadFull(server, buf); err != nil {
				return
			}
			if _, err := server.Write([]byte("PONG")); err != nil {
				return
			}
		}
	}()

	for i := 0; i < 3; i++ {
		if _, err := wrapped.Write([]byte("PING")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(wrapped, buf); err != nil {
			t.Fatalf("Read %d: %v", i, err)
		}
	}
	_ = wrapped.Close()
	<-done

	br, bw, opsR, opsW := stats.Snapshot()
	if bw != 12 {
		t.Errorf("BytesWritten = %d, want 12", bw)
	}
	if br != 12 {
		t.Errorf("BytesRead = %d, want 12", br)
	}
	if opsW != 3 {
		t.Errorf("OpsWritten = %d, want 3", opsW)
	}
	// Each io.ReadFull may issue >=1 underlying Read; over net.Pipe
	// with a 4-byte buffer each ReadFull tends to be a single Read.
	// Assert at-least to stay robust against scheduling.
	if opsR < 3 {
		t.Errorf("OpsRead = %d, want >= 3", opsR)
	}
}

// TestCountingConnErrorPassthrough confirms a Read returning an error
// still bumps OpsRead but only adds the bytes it actually returned, and
// that the error itself is forwarded unchanged.
func TestCountingConnErrorPassthrough(t *testing.T) {
	stats := &MountStats{}
	want := errors.New("boom")
	fake := &fakeConn{readErr: want, readBytes: []byte("abc")}
	wrapped := WrapConn(fake, stats)

	buf := make([]byte, 16)
	n, err := wrapped.Read(buf)
	if !errors.Is(err, want) {
		t.Fatalf("Read err = %v, want %v", err, want)
	}
	if n != 3 {
		t.Fatalf("Read n = %d, want 3", n)
	}
	br, _, opsR, _ := stats.Snapshot()
	if br != 3 {
		t.Errorf("BytesRead = %d, want 3", br)
	}
	if opsR != 1 {
		t.Errorf("OpsRead = %d, want 1", opsR)
	}
}

// TestCountingConnClosePassthrough makes sure Close hits the inner
// conn's Close (verified via fakeConn.closed) and forwards any error.
func TestCountingConnClosePassthrough(t *testing.T) {
	stats := &MountStats{}
	want := errors.New("close-fail")
	fake := &fakeConn{closeErr: want}
	wrapped := WrapConn(fake, stats)

	if err := wrapped.Close(); !errors.Is(err, want) {
		t.Fatalf("Close err = %v, want %v", err, want)
	}
	if !fake.closed {
		t.Error("Close did not reach inner conn")
	}
}

// TestWrapConnNilReturnsNil avoids panicking when callers haven't
// established a connection yet.
func TestWrapConnNilReturnsNil(t *testing.T) {
	if got := WrapConn(nil, &MountStats{}); got != nil {
		t.Fatalf("WrapConn(nil) = %v, want nil", got)
	}
}

// fakeConn is a minimal net.Conn used to drive error / close paths
// without depending on goroutine timing.
type fakeConn struct {
	readBytes []byte
	readErr   error
	closeErr  error
	closed    bool
}

func (f *fakeConn) Read(p []byte) (int, error) {
	n := copy(p, f.readBytes)
	return n, f.readErr
}
func (f *fakeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (f *fakeConn) Close() error                     { f.closed = true; return f.closeErr }
func (f *fakeConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (f *fakeConn) RemoteAddr() net.Addr             { return dummyAddr{} }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "fake" }
func (dummyAddr) String() string  { return "fake" }
