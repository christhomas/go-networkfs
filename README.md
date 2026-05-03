# go-networkfs — multi-protocol network filesystem clients (Go)

Eight network filesystem drivers — FTP, SFTP, SMB, Dropbox, WebDAV,
Google Drive, S3, OneDrive — behind one Go interface (`api.Driver`).
Builds either as a Go library or as `c-archive` static `.a` files (one
per driver, plus a combined `libnetworkfs.a` dispatcher) for embedding
in Swift, Objective-C, C++, or any language that speaks cgo. MIT
licensed — see [LICENSE](LICENSE) and the per-dependency inventory in
the dependency inventory.

## Drivers

| Protocol | Type ID | Status | Upstream client | Notes |
|---|---|---|---|---|
| FTP | 1 | working | `github.com/jlaffaye/ftp` | active/passive, FTPS with system-root verification (`ftps_insecure` / `ftps_ca_file` opt-ins) |
| SFTP | 2 | working | `pkg/sftp` + `x/crypto/ssh` | password + key auth, `known_hosts`, optional SSH agent |
| SMB | 3 | working | `hirochachacha/go-smb2` | SMB2/3, NTLMv2, optional `domain` field |
| Dropbox | 4 | working | `dropbox-sdk-go-unofficial/v6` | OAuth2 access token; thumbnails via `Thumbnailer`. Chunked upload sessions deferred (single-shot upload only) |
| WebDAV | 5 | working | `studio-b12/gowebdav` | streaming uploads; `insecure` opt-in for self-signed |
| Google Drive | 6 | working | stdlib (Drive v3 REST + OAuth2) | proactive + reactive (HTTP 401) token refresh |
| S3 | 7 | working | `minio/minio-go/v7` | S3 + S3-compatible (MinIO, R2, Backblaze, etc.); `path_style`, `prefix`, `secure` knobs |
| OneDrive | 8 | working | stdlib (Microsoft Graph v1.0 + OAuth2) | resumable upload sessions for large files; refresh-token rotation honoured |

All drivers implement `Mount`, `Unmount`, `Stat`, `ListDir`, `OpenFile`,
`CreateFile`, `Mkdir`, `Remove`, `Rename`. Optional capabilities:

- **`Thumbnailer`** — Dropbox only today. Returns provider-rendered
  JPEG/PNG thumbnails in size buckets (32/64/128/256/480/640/960/1024/2048 px).
- **`StatsProvider`** — Dropbox / GDrive / OneDrive (HTTP-based drivers)
  expose per-mount byte/op counters via the shared `CountingTransport`.
  FTP/SFTP/SMB/S3/WebDAV don't yet implement it; callers see harmless
  zero snapshots instead of errors.

### Configuration keys

Passed to `Mount` as `map[string]string` (Go) or a JSON object (C ABI).

| Driver | Keys |
|---|---|
| FTP | `host`, `port`, `user`, `pass`; optional `ftps`, `ftps_insecure`, `ftps_ca_file` |
| SFTP | `host`, `port`, `user`, `pass` or `private_key`; optional `use_ssh_agent`, `insecure_host_key`, `known_hosts` |
| SMB | `host`, `share`, `user`, `pass`; optional `port`, `domain` |
| WebDAV | `url` (or `host`+`port`+`path`), `user`, `pass`; optional `insecure` |
| Dropbox | `access_token` (long-lived; refresh-token support is a follow-up) |
| GDrive | `client_id`, `client_secret`, `refresh_token` |
| S3 | `endpoint`, `region`, `bucket`, `access_key_id`, `secret_access_key`; optional `secure`, `use_path_style`, `prefix` |
| OneDrive | `client_id`, `refresh_token`; optional `client_secret` (empty for PKCE public client) |

## Features

**Transport-level (HTTP drivers):**
- `pkg/api/CountingTransport` — `http.RoundTripper` wrapper that tallies
  request/response body bytes and op counts atomically per mount.
  Surfaced through `MountManager.Stats(mountID)` and
  `networkfs_get_stats(mountID)` in the C ABI.
- HTTP token refresh built into `gdrive` and `onedrive`: proactive
  refresh based on the `expires_in` window plus reactive retry on
  401 with an in-flight body buffer so the retried request matches
  the original.
- Rotation-aware refresh tokens (OneDrive issues a new one per
  refresh; the driver picks it up automatically).

**Driver-level:**
- Streaming reads through `io.ReadCloser`, streaming writes through
  `io.WriteCloser` for SFTP / SMB / WebDAV / S3.
- OneDrive `createUploadSession` for files larger than the simple PUT
  ceiling (chunked PUTs with `Content-Range`, retry per chunk).
- Dropbox provider-side thumbnails via the `Thumbnailer` capability.
- FTPS verifies against system roots by default (no silent
  `InsecureSkipVerify`); `MinVersion` pinned to TLS 1.2.
- SFTP enforces `known_hosts` strict checking unless
  `insecure_host_key=true` is explicit.

**Helpers (`pkg/fsutil`):**
- `Walk` — recursive traversal on top of any `api.Driver`. Honours a
  `SkipDir` sentinel like `filepath.WalkDir`; list-failures are
  delivered to the callback so the walk can resume on peer subtrees.
- `Glob` — pattern matching over `Walk`.

## What works

- Mount / list / stat / read / write / mkdir / rename / remove on every
  driver, exercised by 159 unit + integration tests (see [Test
  contract](#test-contract)).
- `make archives` builds 9 c-archives (8 per-driver + the combined
  `libnetworkfs.a`) cleanly on Linux and macOS in CI.
- TUI file browser (`cmd/tui`) drives every registered driver
  interactively, including config-form generation per driver schema and
  inline image preview (Kitty / iTerm2 / tmux passthrough).
- `.env.yaml`-based account presets for the docker-compose test rig
  (FTP / SFTP / WebDAV / SMB).
- Dropbox / WebDAV / S3 / GDrive / OneDrive integration tests run
  against `httptest.NewServer` fakes — CI exercises the JSON
  envelopes without live credentials.
- SMB integration tests run behind `//go:build smb_integration` against
  a real SMB server (no embeddable Go SMB server exists).

## What doesn't work

- **Dropbox chunked upload sessions** — single-shot upload only. Files
  larger than ~150 MB will fail Dropbox's API limit. Tracked in
  CHANGELOG; no ETA.
- **`context.Context` in the driver interface** — `api.Driver` methods
  don't take a `ctx`, so there's no way to cancel a hanging RETR or
  bound a `ListDir` from the caller side. ROADMAP §4.
- **Random-access reads on the C ABI** — `networkfs_openfile` / 
  `networkfs_writefile` round-trip whole files through a single
  `ByteSlice`. ROADMAP §3 has the handle-based replacement design
  (`fs_open`/`fs_read`/`fs_seek`/`fs_close`); not implemented.
- **`ReadAt` / `Range` reads** — every protocol supports it natively
  (FTP `REST`, SFTP offsets, WebDAV `Range:`, SMB offsets, S3/Dropbox
  `Range:`); the interface just doesn't expose it yet.
- **Connection pooling** — drivers serialise on a single connection per
  mount. Fine for the TUI; pathological for parallel walks. ROADMAP §12.
- **`StatsProvider` on FTP/SFTP/SMB/S3/WebDAV** — only the HTTP-direct
  drivers (Dropbox, GDrive, OneDrive) emit byte counters today.
- **GDrive integration tests** against a fake — only unit tests on pure
  helpers. ROADMAP §11.
- **Symlinks, free-space queries, checksums, atomic
  rename-into-place** — none of these are exposed (ROADMAP §P7).
- **Credential storage** — passwords go through `map[string]string`,
  no `Secret` wrapping. ROADMAP §16.

## Test contract

```
$ go test ./...
ok  cmd/tui          12 tests
ok  dropbox          10 unit + 4 httptest integration
ok  ftp              15 unit + integration (in-process goftp.io server)
ok  gdrive           22 unit (pure helpers; no end-to-end fake yet)
ok  onedrive         30 unit + httptest integration
ok  pkg/api          11 (driver + DriverError)
ok  pkg/api/cgo      6
ok  pkg/fsutil       14 (Walk + Glob)
ok  s3               11 unit + httptest integration
ok  sftp             12 unit + integration (in-process gliderlabs ssh)
ok  smb              7 unit + 4 //go:build smb_integration
ok  webdav           11 unit + httptest integration (x/net/webdav handler)
```

**Total: 159 tests across 12 packages.** All passing on the audit
HEAD. Run with `make test` (race detector + coverage). Run with `make
test-short` to skip suites that bring up embedded servers.

Mocked vs. real:
- **In-process fakes:** Dropbox, OneDrive, GDrive, S3, WebDAV
  integration tests use `httptest.NewServer` fakes that mirror the
  real API JSON envelopes — no live credentials needed in CI.
- **In-process real servers:** FTP (`goftp.io/server/v2`), SFTP
  (`gliderlabs/ssh` + `pkg/sftp`).
- **External services:** SMB (`smb_integration` build tag), Dropbox
  end-to-end (`dropbox_integration` build tag) — both require real
  credentials and are skipped in CI.
- **Docker harness:** `test-server/docker-compose.yml` brings up
  vsftpd / openssh-sftp / apache-webdav / samba on local ports; four
  matching `.env.yaml` presets feed the TUI directly.

## Roadmap

Curated from [docs/ROADMAP.md](docs/ROADMAP.md). Priorities ordered by
when they should land, not by theme.

**P0 — correctness & safety**
- [ ] FTP `Stat` parent-directory listing (current fast path
      misclassifies files as dirs on lenient servers)
- [ ] FTP `CreateFile` streaming via `io.Pipe` (today buffers entire
      file in RAM until `Close`)
- [ ] `context.Context` end-to-end on the `api.Driver` interface
- [ ] Drop `mountID` parameter (one driver per mount; redundant)

**P1 — drivers**
- [ ] Dropbox chunked upload sessions for files > 150 MB
- [ ] httptest fake for GDrive matching the Dropbox/OneDrive pattern
- [ ] Auth-flow docs in `docs/DRIVERS.md` for GDrive / S3 / OneDrive

**P2 — performance**
- [ ] Per-mount connection pooling (configurable size + NOOP keepalive)
- [ ] Opt-in LRU stat/listdir cache with TTL + mutating-op bust
- [ ] `fs_read_into(buf)` zero-copy variant on the C ABI

**P3 — security**
- [ ] `Secret` type replacing plaintext passwords; macOS Keychain /
      Windows DPAPI / Linux Secret Service backends
- [ ] SMB3 encryption forced-on by default

**P5 — packaging**
- [ ] Tag-triggered release workflow producing `darwin-universal`,
      `linux-{amd64,arm64}`, `windows-amd64` archives + `.h` files
- [ ] iOS `.xcframework` + Android `.aar` artefacts

**P7 — capability breadth**
- [ ] `ReadAt(off, len)` for random access (every protocol supports it)
- [ ] `StatFS(mountID) (FSInfo, error)` for free-space queries
- [ ] `Checksum(path, algo)`, `Readlink`/`Symlink`, `RenameOver`

## Changelog

Recent commits (most recent first; see [CHANGELOG.md](CHANGELOG.md) for
the curated history):

```
2026-05-01 61771fc feat(transport): per-mount HTTP byte counters via CountingTransport
2026-05-01 ef6a67e docs: add MIT LICENSE file
2026-04-22 4ba9364 ci: add pre-commit hook (gofmt -s + go vet)
2026-04-21 57a58f7 networkfs_mount: surface classified error messages to callers
2026-04-21 51c23c4 Bump golangci-lint to v2.11.4 and restrict staticcheck to SA* bugs
2026-04-21 96579ef Merge README: upstream structure + my-work sections (TUI/examples/test-server)
2026-04-20 a199fdc Add unit tests for gdrive / s3 / onedrive pure functions
2026-04-20 84dc653 Port build infra / docs / examples / cmd/tui / test-server from my-work
2026-04-20 64b103a Port SFTP / SMB / WebDAV / Dropbox tests to upstream drivers
2026-04-20 ef52423 feat(cmd): add networkfs cgo dispatcher entrypoint
2026-04-20 39e6bd4 feat(onedrive): add OneDrive driver via Microsoft Graph
2026-04-20 48adb84 feat(s3): add S3 driver via MinIO client
2026-04-20 2f2c51a feat(gdrive): add Google Drive driver via Drive v3 REST API
2026-04-20 0ebd149 feat(dropbox): add Dropbox driver
2026-04-20 7dbbc6b feat(webdav): add WebDAV driver
2026-04-20 1654dd2 feat(sftp): add SFTP driver
2026-04-20 f1b6571 feat(smb): add SMB driver
2026-04-18 089355c Restructure for separate minimal libraries
2026-04-18 c4c6771 Initial commit: go-networkfs mono repo structure
```

## License

MIT. See [LICENSE](LICENSE) for the project license and
the dependency inventory for the full per-dependency
license inventory (every direct and transitive dep is permissive —
MIT / BSD-2 / BSD-3 / ISC / Apache-2.0 / weak-copyleft MPL-2.0; no GPL
anywhere).

## Building

```bash
make test            # go test -race with coverage
make test-short      # skip suites that bring up embedded servers
make bench           # streaming + list benches against the embedded FTP server
make archives        # 8 per-driver .a + libnetworkfs.a dispatcher in build/
make tui             # build/networkfs (TUI binary)
make coverage-html   # open HTML coverage report
make vet             # go vet
make tidy            # go mod tidy && go mod verify
```

`make archives` produces:

```
build/libftp.a       libsftp.a       libsmb.a        libdropbox.a
build/libwebdav.a    libgdrive.a     libs3.a         libonedrive.a
build/libnetworkfs.a   <- combined dispatcher (every driver registered)
```

Each archive ships a generated header file (`build/lib<name>.h`)
declaring the C entrypoints.

### Embedding in Go

```go
import (
    "github.com/christhomas/go-networkfs/pkg/api"
    _ "github.com/christhomas/go-networkfs/ftp" // Register driver
)

driver, _ := api.GetDriver(1) // 1 = FTP
driver.Mount(100, map[string]string{
    "host": "ftp.example.com",
    "user": "admin",
    "pass": "secret",
})

info, _ := driver.Stat(100, "/readme.txt")
entries, _ := driver.ListDir(100, "/")
```

Three runnable examples under [examples/](examples) — `list`, `upload`,
`walk`.

### Embedding in Swift / macOS / iOS via cgo

Per-driver archive — link only what you need:

```bash
go build -buildmode=c-archive -o libftp.a ./ftp/cmd/ftp
```

```c
int  ftp_mount(int mount_id, const char* config_json);
int  ftp_unmount(int mount_id);
int  ftp_stat(int mount_id, const char* path, char** out_json);
int  ftp_listdir(int mount_id, const char* path, char** out_json);
int  ftp_openfile(int mount_id, const char* path, ByteSlice* out);
int  ftp_writefile(int mount_id, const char* path, ByteSlice data);
int  ftp_mkdir(int mount_id, const char* path);
int  ftp_remove(int mount_id, const char* path);
int  ftp_rename(int mount_id, const char* old_path, const char* new_path);
void ftp_free(char* ptr);
```

Swap `ftp` for `sftp`, `smb`, `dropbox`, `webdav`, `gdrive`, `s3`, or
`onedrive` — same symbol shape per driver.

Combined dispatcher — one archive routes to any driver by `driver_type`:

```bash
go build -buildmode=c-archive -o libnetworkfs.a ./cmd/networkfs
```

```c
int  networkfs_mount(int mount_id, int driver_type, const char* config_json, char** out_err);
int  networkfs_unmount(int mount_id);
int  networkfs_stat(int mount_id, const char* path, char** out_json);
int  networkfs_listdir(int mount_id, const char* path, char** out_json);
int  networkfs_openfile(int mount_id, const char* path, ByteSlice* out);
int  networkfs_writefile(int mount_id, const char* path, ByteSlice data);
int  networkfs_mkdir(int mount_id, const char* path);
int  networkfs_remove(int mount_id, const char* path);
int  networkfs_rename(int mount_id, const char* old_path, const char* new_path);
int  networkfs_get_thumbnail(int mount_id, const char* path, int size_px, ByteSlice* out);
int  networkfs_get_stats(int32_t mount_id, char** out_json);
int  networkfs_drivers(char** out_json);
char* networkfs_version(void);
void networkfs_free(char* ptr);
```

`networkfs_mount` return codes: `0` success, `1` unknown driver type,
`2` mount failed, `-1` invalid JSON. `networkfs_get_stats` returns
zero counters for drivers that don't implement `StatsProvider`.

In a Swift package, drop the `.a` and `.h` into a binary target and
declare a bridging header:

```swift
.binaryTarget(name: "Networkfs", path: "Frameworks/libnetworkfs.xcframework"),
```

## TUI file browser

A Bubble Tea TUI in [cmd/tui](cmd/tui) for interactive smoke-testing:

```bash
make tui
./build/networkfs                            # interactive driver picker
./build/networkfs --account docker-ftp       # pre-configured (.env.yaml)
./build/networkfs --account docker-ftp /path # non-interactive listing
```

Driver enumeration is dynamic (whatever's registered at link time).
File previews render text inline and emit Kitty / iTerm2 inline-image
escapes when the terminal advertises support; tmux passthrough
(`set -s allow-passthrough on`) is detected via `$TMUX`.

## Test server

[test-server/](test-server) ships a docker-compose harness with FTP /
SFTP / WebDAV / SMB plus fixture data:

```bash
cd test-server && docker compose up -d
```

Default credentials (`testuser` / `testpass`) come from build args in
[test-server/docker-compose.yml](test-server/docker-compose.yml) —
override with `TEST_USER=...` / `TEST_PASSWORD=...`. The matching TUI
presets are in [test-server/.env.yaml](test-server/.env.yaml):

```bash
../build/networkfs --account docker-ftp
../build/networkfs --account docker-sftp
../build/networkfs --account docker-webdav
../build/networkfs --account docker-smb
```

Dropbox / GDrive / OneDrive / S3 aren't in the test server (none can be
self-hosted in a way that matches their real API surface). Their
end-to-end integration tests live behind `//go:build <name>_integration`
tags and require real credentials.

## Architecture

```
pkg/api/              - Shared Driver interface + MountManager + errors
pkg/api/cgo/          - Pure-Go cgo helpers (JSON marshal/unmarshal)
pkg/api/transport.go  - CountingTransport + MountStats + StatsProvider
pkg/fsutil/           - Walk, Glob — free functions on top of api.Driver
cmd/networkfs/        - Combined dispatcher       -> libnetworkfs.a
cmd/tui/              - Bubble Tea file browser   -> build/networkfs
ftp/      ftp/cmd/ftp/           - FTP driver     -> libftp.a
sftp/     sftp/cmd/sftp/         - SFTP driver    -> libsftp.a
smb/      smb/cmd/smb/           - SMB driver     -> libsmb.a
dropbox/  dropbox/cmd/dropbox/   - Dropbox driver -> libdropbox.a
webdav/   webdav/cmd/webdav/     - WebDAV driver  -> libwebdav.a
gdrive/   gdrive/cmd/gdrive/     - GDrive driver  -> libgdrive.a
s3/       s3/cmd/s3/             - S3 driver      -> libs3.a
onedrive/ onedrive/cmd/onedrive/ - OneDrive driver -> libonedrive.a
examples/                        - Runnable Go examples (list/upload/walk)
test-server/                     - docker-compose FTP/SFTP/WebDAV/SMB harness
docs/                            - ROADMAP, DRIVERS, plan notes
```

### Why the cgo helpers are inlined per-main

Each `cmd/*/main.go` carries its own copy of the cgo bridge helpers
(`stringFromC`, `jsonToC`, `setOutBytes`, etc.). When those helpers
live in a separate Go package and their signatures mention `*C.char`,
the `C.char` becomes a package-scoped named type that is not
assignable to `*C.char` in another package's `main`. The cross-package
type identity breaks the build.

`pkg/api/cgo/` therefore exposes only pure-Go helpers (JSON
serialisation, success/error result shapes) that the mains can safely
call. Anything involving `*C.char` — string conversion, byte-slice
pointers, free — has to stay inline in each main.

## Embedding via cgo (host integration)

Vendor as a git submodule (or vendor directly — both work):

```bash
git submodule add https://github.com/christhomas/go-networkfs.git vendor/go-networkfs
```

The included `scripts/build-gonetworkfs.sh` produces all per-driver
libs and the combined `libnetworkfs.a` into a configurable output
directory. Drivers are controlled via the `DRIVERS` env var (default:
`ftp sftp smb dropbox webdav gdrive s3`). Set `BUILD_COMBINED=0` to
skip the combined archive. Set `GO_NETWORKFS_DEBUG=1` to preserve
symbols and DWARF info for debugging from the host side.

## CI

Every push and PR runs (ubuntu-latest + macos-latest matrix):

- `go test -race` with coverage
- `go vet`
- `gofmt -s` check
- `golangci-lint`
- `govulncheck`
- All 9 c-archive builds + the TUI binary

The pre-commit hook (`./scripts/install-hooks.sh`) runs the fast subset
locally — `gofmt -s` + `go vet`. Bypass with `git commit --no-verify`.

## Docs

- [docs/ROADMAP.md](docs/ROADMAP.md) — prioritised plan of what's next
- [docs/DRIVERS.md](docs/DRIVERS.md) — how to write a new driver
- [CHANGELOG.md](CHANGELOG.md) — notable changes
- the dependency inventory — full license inventory
