// pkg/api/transport.go - Per-mount HTTP transport instrumentation.
//
// Why this exists:
//   The Swift FileProvider extension previously hand-instrumented one
//   call site (file content download) and reported the *resulting file
//   size* as bytes-read. Folder enumeration, metadata GETs, OAuth
//   refresh POSTs, and (most painfully) every byte of an upload were
//   invisible to the meter.
//
//   The right place to count is the wire — i.e. the http.RoundTripper.
//   This file provides:
//     • MountStats — atomic per-mount counters.
//     • CountingTransport — http.RoundTripper wrapping a base, counting
//       request body + response body bytes as they stream through.
//     • WrapHTTPClient — convenience helper drivers call in Mount().
//     • StatsProvider — optional interface drivers implement so the
//       MountManager can hand the counters to the C ABI.
//
//   Drivers register their stats via `StatsProvider.Stats()`. The
//   non-HTTP drivers (FTP, SFTP, SMB) don't implement it and the C
//   export returns zeros for those mounts — instrumenting net.Conn for
//   them is future work.

package api

import (
	"io"
	"net/http"
	"sync/atomic"
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
// Drivers without HTTP transports (FTP, SFTP, SMB) currently don't
// implement it; the dispatcher returns a zeroed snapshot for those
// mounts. The MountManager type-asserts on this interface — same
// pattern as Thumbnailer.
type StatsProvider interface {
	Stats() *MountStats
}
