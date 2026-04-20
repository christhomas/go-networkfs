# Writing a driver

A driver is a Go package that implements [`pkg/api.Driver`](../pkg/api/driver.go)
and registers itself in the global driver registry. Downstream consumers
then use the driver either directly (Go import), through the per-driver
C-ABI wrapper (`lib<name>.a`), or through the combined dispatcher
(`libnetworkfs.a`, see [`cmd/networkfs/main.go`](../cmd/networkfs/main.go))
generated for Swift / Objective-C / other language consumers.

The tree currently ships eight drivers: FTP, SFTP, SMB, Dropbox, WebDAV,
Google Drive, S3, and OneDrive. FTP, SFTP, and WebDAV are good working
references for self-hosted servers; Dropbox, Google Drive, and OneDrive
show how to handle OAuth-refreshable tokens; S3 is the reference for
object-store semantics. Stick to the same shape and the CI and combined
dispatcher light up automatically.

## File layout

```
<name>/
  <name>.go            package <name>: driver impl + New + DriverTypeID
  <name>_test.go       unit + integration tests
  cmd/<name>/main.go   package main: cgo wrapper -> lib<name>.a
```

Nothing exotic lives anywhere else. If you need helpers, put them in
unexported functions in `<name>.go`; if they're substantial enough to
deserve their own file, drop it in the same directory.

## What the package must expose

1. **`const DriverTypeID = N`** — a unique integer in the shared range
   documented in [ROADMAP.md](ROADMAP.md). Current assignments: FTP=1,
   SFTP=2, SMB=3, Dropbox=4, WebDAV=5, GDrive=6, S3=7, OneDrive=8.

2. **`func New() api.Driver`** — returns a fresh, unconnected driver.

3. **`init()`** — registers the factory:

   ```go
   func init() {
       api.RegisterDriver(DriverTypeID, New)
   }
   ```

4. **Unexported struct** implementing [`api.Driver`](../pkg/api/driver.go).
   The struct holds all mount state. Name it lowercase (e.g.
   `ftpDriver`) — consumers never touch it directly, so it stays out of
   the public surface.

## Interface contract

Read [`pkg/api/driver.go`](../pkg/api/driver.go) for the authoritative
signatures. Practical notes beyond the types:

- **Paths** are "/-rooted logical paths like `/foo/bar.txt`. The driver
  is responsible for joining them to the mount's `root` config before
  sending them upstream.
- **`Mount(mountID, config)`** is the only place to read config. Missing
  required keys return `&api.DriverError{Code: 1x, Message: "..."}`.
  Don't panic on bad input.
- **`Stat` and `ListDir`** must correctly classify files vs. directories
  (FTP's old List-probe fallback was a bug — see
  [ROADMAP.md §1](ROADMAP.md#1-fix-stat-misclassifying-files-as-directories--s)).
- **`CreateFile`** should stream. If the underlying protocol needs a
  known content length up front, use `io.Pipe` with a goroutine that
  runs the upload, same pattern as [ftp/ftp.go's `CreateFile`](../ftp/ftp.go).
  Buffering the whole payload in RAM is *not* acceptable.
- **`OpenFile`** returns an `io.ReadCloser` — stream the response, don't
  `ReadAll` it into memory.
- **`Remove`** deletes files or empty directories. Don't recurse.
- **Error mapping**: surface the sentinels from
  [`pkg/api/driver.go`](../pkg/api/driver.go) where recognisable
  (`ErrNotFound`, `ErrPermissionDenied`, etc.). Unknown errors pass
  through unwrapped — callers shouldn't have to know your underlying
  library's error types to distinguish kinds.

## Security defaults

Drivers that transport user data are all covered by the same rule:
never accept an unknown trust relationship silently.

- **TLS**: verify certs against system roots by default. Accepting
  self-signed certs requires an explicit opt-in config key (e.g.
  `ftps_insecure=true`). See [ftp/ftp.go's `tlsConfig`](../ftp/ftp.go).
- **SSH host keys**: require a `known_hosts` file or an explicit
  `insecure_host_key=true` — never default-accept any key. See
  [sftp/sftp.go's `buildHostKeyCallback`](../sftp/sftp.go).
- **Credentials**: accept them via the config map for now. A `Secret`
  type abstraction is on the roadmap
  ([ROADMAP.md §16](ROADMAP.md#16-credential-storage-abstraction--m));
  when it lands, migrate.

## Testing

Tests live next to the driver in `<name>_test.go`. Two layers:

1. **Unit tests** for pure helpers (path joining, config parsing, error
   mapping) — no network, fast, always run.
2. **Integration tests** against an in-process server (see the FTP,
   SFTP, WebDAV tests for templates). Use `net.Listen("tcp",
   "127.0.0.1:0")` for an ephemeral port and `t.Cleanup` to tear down.
   For HTTP-API-backed drivers (Dropbox, GDrive, OneDrive, S3), use
   `httptest.NewServer` with a fake that replays the provider's JSON
   envelope — see the Dropbox and GDrive tests for the pattern.

Required coverage:

- All happy paths for `Mount`, `Stat`, `ListDir`, `OpenFile`,
  `CreateFile`, `Mkdir`, `Remove`, `Rename`.
- `Stat` on a file vs. a directory (IsDir must be correct).
- `Stat` on a missing path (error must be returned).
- At least one streaming-write test with a payload ≥ 64 KiB to exercise
  the streaming path rather than the happy-small-file path.
- Auth failure (wrong password, bad token, etc.).
- All the "not connected" guards.

## The C-ABI wrapper

Every driver has a `cmd/<name>/main.go` that exposes its operations via
cgo as `<name>_mount`, `<name>_unmount`, `<name>_stat`, … functions.
The wrappers are ~150 lines each and near-identical between drivers —
see [ftp/cmd/ftp/main.go](../ftp/cmd/ftp/main.go) as the template.

Rules:

- All cgo conversions (`C.CString`, `C.GoString`, `C.CBytes`, `C.free`)
  happen inline in the `main.go`. The `pkg/api/cgo` helpers only deal in
  pure Go types — `*C.char` can't cross package boundaries in cgo.
- Every function that returns memory to the caller allocates it with
  `C.CString` / `C.CBytes` and documents that the caller must free it
  via `<name>_free`.
- Error codes: `0` success, `1` generic failure, `-1` bad input.
  Detailed error text comes back via the `outJSON` out-parameter.

A codegen path for these wrappers is on the roadmap
([ROADMAP.md §19](ROADMAP.md#19-code-generation-for-c-abi-wrappers--m));
until it lands, copy-paste is fine.

## The combined dispatcher

The [`cmd/networkfs`](../cmd/networkfs/main.go) package blank-imports
every driver and exposes a single `networkfs_*(driver_type, ...)`
C-ABI that routes each call to the right backend. When you add a new
driver, add a blank import at the top of `cmd/networkfs/main.go`
alongside the existing ones — that's all the dispatcher needs to see a
new type id.

Consumers can either link the per-driver archives (`libftp.a`, `libs3.a`,
etc.) individually or link the combined `libnetworkfs.a` and get every
backend in one go.

## CI hookup

The [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) file
enumerates every driver for the `build-archives` job. Add a new entry
alongside the existing ones — one line per driver — and add the same
blank import to `cmd/networkfs/main.go` so the combined archive keeps
covering everything.
