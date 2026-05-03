// pkg/api/transport.go - Per-mount transport instrumentation.
//
// Why this exists:
//   Consumers of this library historically hand-instrumented one call
//   site (file content download) and reported the *resulting file
//   size* as bytes-read. Folder enumeration, metadata GETs, OAuth
//   refresh POSTs, and (most painfully) every byte of an upload were
//   invisible to the meter.
//
//   The right place to count is the wire — for HTTP that is the
//   http.RoundTripper, and for socket-protocol drivers (FTP, SFTP,
//   SMB) that is the underlying net.Conn. This file provides:
//     • MountStats — atomic per-mount counters.
//     • CountingTransport — http.RoundTripper wrapping a base, counting
//       request body + response body bytes as they stream through.
//     • WrapHTTPClient — convenience helper HTTP drivers call in Mount().
//     • CountingConn — net.Conn wrapper counting Read/Write bytes and
//       op counts for the socket-protocol drivers.
//     • WrapConn — convenience helper for socket drivers that returns
//       a counting net.Conn.
//     • StatsProvider — optional interface drivers implement so the
//       MountManager can hand the counters to the C ABI.
//
//   Drivers register their stats via `StatsProvider.Stats()`. Drivers
//   that don't implement the interface get zeroed snapshots from the
//   dispatcher.

package api

import (
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// MountStats holds atomic byte/op counters for a single mount's
// network transport. All accesses are lock-free via atomic ops so the
// hot HTTP path doesn't contend with the stats reader.
//
// Counters are monotonic — they only ever grow for the lifetime of
// the mount. The host-side `IOStats.absorb` derives throughput by
// diffing successive snapshots and resets its baseline if the
// counters appear to go backwards (e.g. the Go process restarted).
type MountStats struct {
	BytesRead    uint64 // bytes received from the server (response bodies)
	BytesWritten uint64 // bytes sent to the server (request bodies)
	OpsRead      uint64 // count of completed responses
	OpsWritten   uint64 // count of issued requests
}

// Snapshot returns a consistent read of the four counters.
func (s *MountStats) Snapshot() (bytesRead, bytesWritten, opsRead, opsWritten uint64) {
	return atomic.LoadUint64(&s.BytesRead),
		atomic.LoadUint64(&s.BytesWritten),
		atomic.LoadUint64(&s.OpsRead),
		atomic.LoadUint64(&s.OpsWritten)
}

// CountingTransport wraps a base RoundTripper and tallies every byte
// of every request/response body that flows through it.
//
// Bytes are counted as the body is *streamed*, not when the request
// is built. That matters because Go's http package reads request
// bodies lazily and writes responses incrementally — a Content-Length
// header is not always available, and even when it is, retries and
// redirects re-read the body. The streaming counter handles all of it
// without special cases.
//
// Headers are *not* counted. They're a small, mostly-fixed overhead
// per request, and including them would require us to serialize the
// outgoing headers and intercept the connection's HTTP framing — not
// worth the complexity for a UI throughput display.
type CountingTransport struct {
	base  http.RoundTripper
	stats *MountStats
}

// NewCountingTransport returns a RoundTripper that counts body bytes
// against `stats`. If `base` is nil, http.DefaultTransport is used so
// callers can pass `&http.Client{}` without first having to construct
// a Transport themselves.
func NewCountingTransport(base http.RoundTripper, stats *MountStats) *CountingTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &CountingTransport{base: base, stats: stats}
}

// RoundTrip implements http.RoundTripper. It wraps req.Body and
// resp.Body in counting readers, increments OpsWritten before the
// base round-trip and OpsRead after a successful one. Errors do not
// bump OpsRead — the failed request already contributed to OpsWritten,
// so a failed RT shows up as a write op with no matching read.
func (t *CountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body = &countingReadCloser{rc: req.Body, n: &t.stats.BytesWritten}
	}
	atomic.AddUint64(&t.stats.OpsWritten, 1)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	atomic.AddUint64(&t.stats.OpsRead, 1)
	if resp.Body != nil {
		resp.Body = &countingReadCloser{rc: resp.Body, n: &t.stats.BytesRead}
	}
	return resp, nil
}

// countingReadCloser wraps an io.ReadCloser and atomically adds every
// successfully-read byte to *n. Errors are passed through untouched —
// partial reads still count for what they returned, matching the
// io.Reader contract.
type countingReadCloser struct {
	rc io.ReadCloser
	n  *uint64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		atomic.AddUint64(c.n, uint64(n))
	}
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }

// WrapHTTPClient returns `client` with its Transport replaced by a
// CountingTransport that wraps the previous Transport. The original
// Transport (or http.DefaultTransport when nil) becomes the base, so
// drivers can stack this on top of OAuth-refreshing transports etc.
//
// Drivers should call this once in Mount() right after constructing
// the *http.Client and store the returned `*MountStats` so they can
// surface it via StatsProvider.
func WrapHTTPClient(client *http.Client, stats *MountStats) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	client.Transport = NewCountingTransport(client.Transport, stats)
	return client
}

// StatsProvider is the optional capability drivers implement to expose
// their per-mount transport counters to the MountManager / C ABI.
// Drivers that don't implement it return a zeroed snapshot from the
// dispatcher. The MountManager type-asserts on this interface — same
// pattern as Thumbnailer.
type StatsProvider interface {
	Stats() *MountStats
}

// CountingConn wraps a net.Conn and atomically tallies every byte
// read or written, plus the count of Read/Write *calls*. Bytes are
// counted on success; partial reads/writes still contribute the bytes
// they did move, matching the io.Reader / io.Writer contract.
//
// Op semantics differ from CountingTransport on purpose:
//   - For HTTP, an "op" is a request/response pair, so OpsWritten /
//     OpsRead increment per RoundTrip.
//   - For raw connections there is no notion of a request boundary, so
//     OpsRead increments once per Read syscall and OpsWritten once
//     per Write syscall (regardless of size). Throughput readers
//     should treat the op counters as a coarse activity signal, not a
//     transactional metric, when the underlying transport is a socket.
//
// All methods are concurrency-safe to the same extent the wrapped
// net.Conn is — the counter updates use atomics and don't introduce
// any new locking.
type CountingConn struct {
	net.Conn
	stats *MountStats
}

// WrapConn returns c wrapped in a CountingConn that updates stats on
// every successful Read/Write. Passing a nil stats is a programming
// error and will panic on first use; callers should always allocate
// a MountStats in Mount() before wrapping.
func WrapConn(c net.Conn, stats *MountStats) net.Conn {
	if c == nil {
		return nil
	}
	return &CountingConn{Conn: c, stats: stats}
}

// Read forwards to the underlying conn and atomically adds the bytes
// returned to BytesRead and increments OpsRead by one. Errors are
// returned unchanged; partial reads still count for what they
// returned.
func (c *CountingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		atomic.AddUint64(&c.stats.BytesRead, uint64(n))
	}
	atomic.AddUint64(&c.stats.OpsRead, 1)
	return n, err
}

// Write forwards to the underlying conn and atomically adds the bytes
// written to BytesWritten and increments OpsWritten by one. Errors are
// returned unchanged; partial writes still count for what they wrote.
func (c *CountingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		atomic.AddUint64(&c.stats.BytesWritten, uint64(n))
	}
	atomic.AddUint64(&c.stats.OpsWritten, 1)
	return n, err
}

// Close just forwards to the underlying conn; counters are not reset.
// (MountStats lives on the driver and survives reconnects.)
func (c *CountingConn) Close() error { return c.Conn.Close() }

// LocalAddr / RemoteAddr / SetDeadline / SetReadDeadline /
// SetWriteDeadline are inherited via the embedded net.Conn so the
// wrapper is a drop-in replacement for any caller that uses the full
// net.Conn surface.
//
// The methods below are declared explicitly only for the rare TLS
// stack that uses a type switch instead of the interface — embedding
// alone covers the interface case.

// SetDeadline forwards to the underlying conn.
func (c *CountingConn) SetDeadline(t time.Time) error { return c.Conn.SetDeadline(t) }

// SetReadDeadline forwards to the underlying conn.
func (c *CountingConn) SetReadDeadline(t time.Time) error { return c.Conn.SetReadDeadline(t) }

// SetWriteDeadline forwards to the underlying conn.
func (c *CountingConn) SetWriteDeadline(t time.Time) error { return c.Conn.SetWriteDeadline(t) }
