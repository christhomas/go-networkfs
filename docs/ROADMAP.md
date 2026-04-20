# go-networkfs — Roadmap

A prioritised plan for making this library actually useful as the backing
layer for DiskJockey and any other consumer that wants "one API, N remote
filesystems." Effort is a rough guide: **S** ≤ 1 day, **M** 1–3 days,
**L** 1 week+.

Sections are ordered by when they should land, not by theme.

---

## P0 — correctness and safety (ship first)

These are actual bugs or footguns that will bite real users. Do these
before anything else.

### 1. Fix `Stat` misclassifying files as directories · S
[ftp/ftp.go](../ftp/ftp.go) falls back to `client.List(absPath)` when
`GetEntry` fails, and unconditionally marks the entry as `IsDir: true`. On
lenient servers (including our `goftp.io/server/v2` test rig) `List` on a
file path succeeds, so every such Stat returns the wrong type. The FTP
test currently has an assertion relaxed to hide this.

**Fix:** `Stat` should list the *parent* directory and find the entry
whose name matches the basename. This works on every FTP server. Keep
the fast path on `GetEntry` for servers that support MLST.

### 2. Stop silently disabling TLS verification for FTPS · S
[ftp/ftp.go:99](../ftp/ftp.go#L99) hardcodes `InsecureSkipVerify: true`.
That turns FTPS into opportunistic encryption — a MITM can trivially
impersonate the server. Accepting self-signed certs is a legitimate need,
but it must be opt-in:

```
"ftps": "true",
"ftps_insecure": "true",   // only then skip verification
"ftps_ca_file": "/path.pem"  // pin a CA bundle
```

### 3. Streaming I/O across the public API · M
Two memory hogs today:

- `ftpWriter` in [ftp/ftp.go:259](../ftp/ftp.go#L259) buffers the **entire**
  file in RAM until `Close()`, then calls `STOR`. Uploading a 4 GB backup
  uses 4 GB of RAM.
- `ftp_openfile` / `ftp_writefile` in [ftp/cmd/ftp/main.go](../ftp/cmd/ftp/main.go)
  round-trip the whole file through a single `ByteSlice`. Same problem
  plus an extra Go→C copy.

**Fix:** pipe through `io.Pipe` so `Write` on the returned `WriteCloser`
sends directly over the data connection; close stops the STOR. On the
C-ABI, replace the one-shot byte slice exports with a handle-based API:

```
int64_t fs_open(int mount_id, const char* path, int mode);
int     fs_read(int64_t handle, void* buf, size_t len, size_t* out_n);
int     fs_write(int64_t handle, const void* buf, size_t len);
int     fs_seek(int64_t handle, int64_t off, int whence, int64_t* new_off);
int     fs_close(int64_t handle);
```

### 4. Propagate `context.Context` end-to-end · M
No method on [pkg/api.Driver](../pkg/api/driver.go) accepts a context,
so there's no way to cancel a hanging `RETR` or bound a `ListDir`. TUI
and future consumers need this.

**Fix:** extend the interface (this is the one moment to break it — we
have one real implementation):

```go
Mount(ctx context.Context, mountID int, cfg map[string]string) error
Stat(ctx context.Context, mountID int, path string) (FileInfo, error)
...
```

Drivers wrap the underlying client calls; c-archive wrappers create a
context per call with a configurable deadline.

### 5. Reconcile "one driver per mount" vs. the interface signature · S
`ftpDriver` stores `client`, `host`, `rootPath` as instance fields, so it
can only serve one mount. But every method still takes `mountID`. The
`MountManager` already picks one driver per mount, so `mountID` is dead
weight.

**Pick one:**
- Drop `mountID` from the interface and let `MountManager` own the map
  (cleanest — removes the parameter everywhere).
- Keep `mountID` and make drivers hold `map[int]*session` internally
  (better if we ever want connection sharing across mounts).

I'd drop it.

### 6. The `DriverError` value-vs-pointer trap · S
`Error()` has a pointer receiver, so `return api.DriverError{…}` silently
fails to compile as an `error`. We already hit this during the cgo
refactor. Either give `Error()` a value receiver, or add a vet-lint CI
step that catches it.

---

## P1 — implementations (unlock real value)

**Status: mostly landed.** The driver registry grew from five slots to
eight; all eight are now real implementations rather than stubs. FTP,
SFTP, SMB, Dropbox, and WebDAV came from this branch's work; Google
Drive, S3, and OneDrive landed from the upstream merge. The historical
notes below are retained for context on each driver's library choice
and config shape.

### 7. SFTP driver · M — DONE
Shipped on `github.com/pkg/sftp` over `golang.org/x/crypto/ssh`.
Integration tests run against `github.com/gliderlabs/ssh` in-process.

### 8. WebDAV driver · S — DONE
Shipped on `github.com/studio-b12/gowebdav`. Integration tests run
against `golang.org/x/net/webdav` via `httptest.NewServer`.

### 9. SMB driver · M — DONE
Shipped on `github.com/hirochachacha/go-smb2`. Integration tests live
behind a build tag (`smb_integration`) because no embeddable Go SMB
server exists.

### 10. Dropbox driver · M — DONE
Shipped as a raw v2 HTTP client. Integration tests run against an
`httptest.Server` fake. Token refresh story still owed — the config map
takes a pre-minted access token for now (see §16 for the longer-term
secret-storage plan).

### 11. Upstream drivers stabilisation · S
Google Drive (type 6), S3 (type 7), and OneDrive (type 8) landed in
the upstream merge. Each refreshes OAuth tokens transparently where
the provider requires it; the combined `libnetworkfs.a` dispatcher
covers them alongside the original five. Remaining work:

- Port the httptest-based integration-test pattern (Dropbox /
  WebDAV style) to any of the three drivers that don't already have
  one, so CI exercises their JSON envelopes without live credentials.
- Document auth flows (service-account JSON for GDrive, IAM credentials
  for S3, device-code OAuth for OneDrive) in `docs/DRIVERS.md`.
- Audit their error-mapping against the shared sentinels in
  `pkg/api/driver.go` so consumers can distinguish `ErrNotFound` from a
  generic `DriverError` consistently.

---

## P2 — performance

### 12. Connection pooling and keep-alive · M
Every operation on the FTP driver reuses one `ServerConn`. That's fine
for the TUI but pathological for parallel workloads: a recursive walk
serialises on one command channel. Add a per-mount pool (sized by
config) and a `NOOP` keep-alive goroutine so connections survive idle
timeouts.

### 13. Stat / ListDir cache · M
For a file-browser UI, walking the same tree repeatedly is common. Add
an opt-in LRU keyed by `(mountID, path)` with a short TTL (5–30s). Bust
on any mutating op (`Mkdir`, `Remove`, `Rename`, `CreateFile`).

### 14. Benchmarks · S
Nothing in the repo measures throughput. Add `go test -bench` for the
FTP driver against the embedded server:

- stream upload throughput (1 MB, 10 MB, 100 MB)
- ListDir on a 10 000-file directory
- Stat on cold vs. warm cache (after §13)

Benchmarks give §3 and §12 a numeric target instead of a vibe.

### 15. Zero-copy on the C boundary · M
The current C-ABI always goes `[]byte → C.CBytes → C.free`. For reads
larger than ~1 MB, expose a `fs_read_into(handle, void* buf, size_t
len)` variant where the caller supplies the buffer. Avoids the double
allocation.

---

## P3 — security and secrets

### 16. Credential storage abstraction · M
Plaintext passwords in `map[string]string` is a bad default — they'll
end up in crash dumps, process listings, and log lines. Define a
`Secret` type:

```go
type Secret interface { Reveal() string; Zero() }
```

Drivers take `Secret` instead of plain strings; in-memory implementation
wipes the bytes on `Zero`. For host apps (DiskJockey), wire up Keychain
(macOS) / Keystore (Android) / DPAPI (Windows) implementations.

### 17. SMB3 encryption / SSH strict host-key checking · S
When SMB and SFTP land, force-on the safe defaults: SMB3 encryption
negotiated, `known_hosts` checked by default. Same pattern as §2 — opt
out explicitly, not by default.

### 18. `govulncheck` in CI · S
One-line workflow addition. Catches known CVEs in transitive deps (SSH,
TLS, compression, parsing — all areas we pull in as the drivers land).

---

## P4 — developer experience

### 19. Code generation for C-ABI wrappers · M
The five `cmd/*/main.go` files are ~150 lines of near-identical cgo
boilerplate. Each new driver adds another copy; every API change edits
five files. Generate them from a single template and a tiny spec file:

```yaml
# driver.gen.yaml
name: sftp
type_id: 2
package: github.com/christhomas/go-networkfs/sftp
```

Commit the generated files so downstream builds don't need the
generator, but fail CI if the generated output is stale (`go generate
./... && git diff --exit-code`).

### 20. Makefile / Taskfile · S
Common invocations are verbose. A tiny `Makefile` with targets:

```
make test           # go test ./... -race -cover
make bench          # go test -bench=. ./ftp
make archives       # all five .a files
make tui            # build the TUI
make coverage-html  # open coverage.html
```

### 21. Linting · S
Add `golangci-lint` with a conservative config (govet, staticcheck,
errcheck, gofmt, unused, revive). Run in CI. Catches most of the §6
class of bugs at review time.

### 22. `examples/` directory and executable godoc · S
One example per use case:

- `examples/list/` — list a remote directory
- `examples/upload/` — stream a file
- `examples/walk/` — recursive walk

Each `main.go` kept short enough to read in one screen; they double as
copy-paste starter code.

### 23. "How to write a driver" guide · S
A short `docs/DRIVERS.md` covering:

- the five required files (`<name>/<name>.go`, `<name>/<name>_test.go`,
  `<name>/cmd/<name>/main.go`, plus schema entry in the TUI)
- error mapping (what wraps `ErrNotFound` vs. a generic `DriverError`)
- the interface contract (which methods must be idempotent, what paths
  look like, how roots are joined)
- how to run integration tests locally

---

## P5 — platform packaging

### 24. Release workflow producing per-platform archives · M
The DiskJockey integration vendors this repo and builds the archives in
its own Xcode step. That's fine for one consumer; the second consumer
will want prebuilt artefacts. A tag-triggered GitHub Actions job:

- `macOS-13` / `macOS-14` runners: build `libftp.a` etc. for
  `darwin/amd64` and `darwin/arm64`, `lipo` into universal binaries,
  attach to the release.
- `ubuntu-latest`: `linux/amd64` + `linux/arm64` (via
  `CC=aarch64-linux-gnu-gcc`).
- `windows-latest`: `windows/amd64` (`.lib` not `.a`).
- Bundle each with its generated `.h` file.

### 25. iOS / Android targets · L
For mobile consumers, produce:

- an `.xcframework` containing static archives for
  `ios-arm64`, `ios-arm64-simulator`, `ios-x86_64-simulator`
- Android `.aar` via `gomobile bind` or a raw `.so` per ABI
  (`arm64-v8a`, `armeabi-v7a`, `x86_64`)

Hooked into the same release workflow.

### 26. Semver + CHANGELOG · S
Start tagging `v0.1.0` now while the API is still fluid; bump minor on
every interface change (P0 §4 and §5 will be the first breaks). Keep a
hand-written `CHANGELOG.md` — `git log` is not a changelog.

---

## P6 — observability

### 27. Structured logging hook · S
Drivers are currently silent. Add a logger interface on `api.Driver`
(optional — set via `WithLogger(...)`) so consumers can see retries,
reconnects, slow ops. Use `log/slog` (std lib) so we don't pick a
flavour for downstream users.

### 28. Metrics / tracing hooks · M
A tiny `Observer` interface with `OpStart(op, path)` /
`OpEnd(op, path, err, dur)`. Host apps wire it to OpenTelemetry or
Prometheus if they care; default is a no-op. Same pattern as §27.

---

## P7 — capability breadth

These aren't urgent but they're what real filesystems have. Implement
when a consumer actually needs them.

- **Random access** (`ReadAt(off, len)`) — FTP has `REST`, SFTP has it
  natively, WebDAV has `Range:` headers, SMB has offsets, Dropbox has
  `Range:`. All five support it; the interface just doesn't expose it
  yet.
- **Symlinks** (`Readlink`, `Symlink`) — SFTP has it, WebDAV doesn't,
  FTP sort-of via `SITE SYMLINK`. Add to the interface with an
  `ErrNotSupported` path for drivers that can't.
- **Free space / quota** — `StatFS(mountID) (FSInfo, error)`.
- **Recursive Walk** — a free function on top of the interface, not a
  driver method; `filepath.WalkDir` equivalent.
- **Glob** / **search** — same: free function.
- **Checksums** — `Checksum(mountID, path, algo)`; FTP has `XCRC`,
  Dropbox exposes content hashes natively, SFTP has extensions.
- **Atomic rename-into-place** — most protocols support atomic rename
  over an existing target; a single `RenameOver` helper is worth it.

---

## P8 — housekeeping

Small cleanups that don't need a roadmap entry but should land soon:

- Remove the stray empty `tui/` directory at repo root (artifact of a
  `go build ./cmd/tui` run without `-o`).
- Move the cgo `cmd/*/main.go` files into a build tag (`//go:build
  cgo_archive`) so coverage numbers reflect only non-generated code —
  current 29.1% total is misleading.
- Add a matrix cell for `macOS-latest` and `windows-latest` in CI to
  catch platform-specific path handling before consumers do.
- README currently implies a unified `cmd/nfs` dispatcher that doesn't
  exist; either build it or remove the section.

---

## Suggested execution order

If I had to pick one thing to do each week:

1. Week 1 — P0 §1, §2, §5, §6 (bug sweep, one day each).
2. Week 2 — P0 §3 streaming I/O + P0 §4 context plumbing (interface
   break window; do them together).
3. Week 3 — P1 §7 SFTP (biggest missing driver by far).
4. Week 4 — P1 §8 WebDAV + P4 §19 codegen (WebDAV is fast, codegen
   tames the cgo duplication before SMB/Dropbox land).
5. Week 5 — P1 §9 SMB, §10 Dropbox.
6. Week 6 — release workflow (P5 §24).

After that the priorities are driven by whatever the first real
consumer (DiskJockey) hits first.
