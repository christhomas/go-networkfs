# Changelog

All notable changes to this project are documented here. Dates are
ISO-8601. Changes under `Unreleased` haven't been tagged yet.

## Unreleased

### Added
- **Three new drivers merged from upstream**: Google Drive (type id 6),
  S3 (type id 7), OneDrive (type id 8). All three refresh OAuth tokens
  transparently where the provider requires it.
- **Combined dispatcher** (`libnetworkfs.a` under `cmd/networkfs`): one
  static archive that blank-imports every driver and exposes a
  `networkfs_*(driver_type, ...)` routing API. Consumers no longer have
  to link eight separate archives to get every backend.
- **Agent-ported test suites** (60 tests total) exercising all five
  pre-existing drivers (FTP, SFTP, SMB, Dropbox, WebDAV) against the
  upstream implementations, catching regressions in the merged tree.
- **Examples updated** to blank-import all eight drivers so
  `examples/list`, `examples/upload`, and `examples/walk` work against
  every registered driver type at runtime.

## 0.2.0-dev — pre-merge my-work history

The entries below documented work on the `my-work` branch prior to the
upstream merge described above. They have not been released under a
version tag yet; they are preserved here for context.

### Added
- **Dropbox driver** (raw v2 HTTP): `Stat`, `ListDir` (with pagination),
  `Mkdir`, `Remove`, `Rename`, `OpenFile`, `CreateFile`. Config keys
  include `api_endpoint` / `content_endpoint` so tests can swap the
  production URLs for an `httptest.Server` — 15 integration tests run
  against an in-process fake without ever touching real Dropbox.
  Chunked upload sessions (files > 150 MB) are deferred.
- **SMB driver** (hirochachacha/go-smb2, SMB2/3 + NTLMv2): full API
  coverage with streaming `*File` for constant-memory up/download. Unit
  tests cover helpers and guards; integration tests live behind
  `//go:build smb_integration` because no embeddable Go SMB server
  exists — run locally with `SMB_HOST` / `SMB_SHARE` / `SMB_USER` /
  `SMB_PASS` env vars.
- **`pkg/fsutil.Walk`**: recursive traversal helper on top of any
  `api.Driver`. `fsutil.SkipDir` behaves like `filepath.SkipDir`;
  list-failures are delivered to the callback so fn can swallow them
  and keep walking peer subtrees. `examples/walk` rewritten on top of
  it.
- **`hack/genabi`** code generator: single template drives all five
  `cmd/<name>/main.go` cgo wrappers. `make gen` regenerates;
  `make gen-check` fails on drift; CI runs `-check` on every PR.
- **CI matrix**: `test` and `build-archives` jobs now run on both
  `ubuntu-latest` and `macos-latest`.
- **SFTP driver** (pkg/sftp over x/crypto/ssh): password + key auth,
  strict host-key checking via known_hosts, `insecure_host_key` opt-in,
  streaming I/O. Integration tests run against an in-process SSH + SFTP
  subsystem.
- **WebDAV driver** (studio-b12/gowebdav): full API coverage including
  streaming uploads. Integration tests run against an in-process
  `golang.org/x/net/webdav` handler on `httptest`.
- **TUI file browser** in `cmd/tui` using Bubble Tea: driver picker,
  config form per driver schema, file browser with navigation.
- **Makefile** with targets for test, bench, archives, tui,
  coverage-html, vet, tidy, clean.
- **CI pipeline**: `go vet`, `go test -race` + coverage, `gofmt -s`
  check, `golangci-lint`, `govulncheck`, and per-driver c-archive
  builds.
- **Benchmarks** for FTP streaming upload and list under `ftp/bench_test.go`.
- **Examples**: `list`, `upload`, `walk` under `examples/`.
- **Docs**: `docs/ROADMAP.md` (8-tier prioritised plan) and
  `docs/DRIVERS.md` (contributor guide for writing new drivers).

### Changed
- **TUI config schemas** updated: SFTP exposes `insecure_host_key`, SMB
  adds `port` + `domain`, WebDAV adds `insecure`, Dropbox clarifies
  that blank root means the Dropbox root.
- **SMB dial address** uses `net.JoinHostPort` so IPv6 host names are
  bracketed correctly.
- **FTPS TLS**: verify server cert against system roots by default. New
  config keys `ftps_insecure` (opt-in skip) and `ftps_ca_file` (PEM
  pinning) replace the silent `InsecureSkipVerify: true`.
  `MinVersion` pinned to TLS 1.2.
- **FTP upload I/O**: `ftpWriter` now streams through `io.Pipe` instead
  of buffering the entire payload in RAM. Memory stays constant
  regardless of file size.
- **FTP `Stat`** classifies files vs. directories correctly. The old
  List-probe fallback marked every result as a directory on lenient
  servers.
- **pkg/api/cgo/bridge.go** uses pure-Go types across the package
  boundary. The old `*C.char` signatures couldn't cross package
  boundaries in cgo, which meant no c-archive actually built.
- **`DriverError.Error`** uses a value receiver so both `DriverError{}`
  and `&DriverError{}` satisfy `error`.
- **Driver struct naming**: `FTPDriver` → unexported `ftpDriver`.
  Consumers get `ftp.New()` + `ftp.DriverTypeID` only.

### Removed
- `tui` binary that was accidentally committed to the repo root
  (replaced by `build/networkfs` per the Makefile).

### Fixed
- `jlaffaye/ftp` pinned to `v0.2.0` (the prior `v1.0.0` doesn't exist
  upstream and broke `go mod tidy`).
- `ftp.ftpDriver.Mount` returns an actual `error`. Previous code
  returned `api.DriverError{…}` by value against a pointer-receiver
  `Error()`, which didn't satisfy the interface.

## 0.1.0 — initial public drop

Bootstrap commit of the module structure:

- `pkg/api` — Driver interface, FileInfo, error sentinels, MountManager.
- `pkg/api/cgo` — C bridge helpers (initial draft, pre-refactor).
- `ftp/` — FTP driver skeleton migrated from diskjockey-backend.
- `ftp/cmd/ftp/main.go` — first cgo wrapper draft.
- `sftp/`, `smb/`, `dropbox/`, `webdav/` — stubs that register with
  the driver registry and return "not implemented" for every method.
