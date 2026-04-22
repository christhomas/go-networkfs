# go-networkfs

Network filesystem drivers for Go, with C library exports for Swift /
Objective-C integration.

Every driver implements the shared `api.Driver` interface (see
[`pkg/api/driver.go`](pkg/api/driver.go)) and registers itself with a
process-wide registry via `init()`. Two build outputs are produced:

- **Per-driver static libs** (`libftp.a`, `libsftp.a`, …) — each exports
  only that driver's C symbols (`ftp_mount`, `sftp_mount`, …). Link only
  what you need; smaller binaries.
- **Combined static lib** (`libnetworkfs.a`) — blank-imports every driver
  and exposes a single dispatcher API (`networkfs_mount(mount_id,
  driver_type, config_json)`, `networkfs_stat(...)`, …) that routes by
  `driver_type` at mount time.

## Drivers

| Type     | ID | Package                  | Upstream dependency                               |
|----------|----|--------------------------|---------------------------------------------------|
| FTP      |  1 | `go-networkfs/ftp`       | `github.com/jlaffaye/ftp`                         |
| SFTP     |  2 | `go-networkfs/sftp`      | `github.com/pkg/sftp` + `golang.org/x/crypto`     |
| SMB      |  3 | `go-networkfs/smb`       | `github.com/hirochachacha/go-smb2`                |
| Dropbox  |  4 | `go-networkfs/dropbox`   | `github.com/dropbox/dropbox-sdk-go-unofficial/v6` |
| WebDAV   |  5 | `go-networkfs/webdav`    | `github.com/studio-b12/gowebdav`                  |
| GDrive   |  6 | `go-networkfs/gdrive`    | stdlib only (Drive v3 REST, OAuth2 refresh)       |
| S3       |  7 | `go-networkfs/s3`        | `github.com/minio/minio-go/v7`                    |
| OneDrive |  8 | `go-networkfs/onedrive`  | stdlib only (Microsoft Graph v1.0, OAuth2 refresh)|

All drivers implement: `Mount`, `Unmount`, `Stat`, `ListDir`, `OpenFile`,
`CreateFile`, `Mkdir`, `Remove`, `Rename`.

### Configuration keys

Passed to `Mount` as a `map[string]string` (Go) or JSON object (C):

| Driver   | Keys                                                                 |
|----------|----------------------------------------------------------------------|
| FTP      | `host`, `port`, `user`, `pass`; optional `tls` (`"explicit"` for FTPS) |
| SFTP     | `host`, `port`, `user`, and either `pass` or `private_key`; optional `use_ssh_agent` |
| SMB      | `host`, `share`, `user`, `pass`; optional `domain`                   |
| WebDAV   | `url` (or `host`+`port`+`path`), `user`, `pass`                      |
| Dropbox  | `access_token` (long-lived; refresh-token support is a follow-up)    |
| GDrive   | `client_id`, `client_secret`, `refresh_token`                        |
| S3       | `endpoint`, `region`, `bucket`, `access_key_id`, `secret_access_key`; optional `secure`, `use_path_style`, `prefix` |
| OneDrive | `client_id`, `refresh_token`; optional `client_secret` (empty for PKCE public client) |

GDrive and OneDrive refresh access tokens on demand — proactively from
the `expires_in` response field and reactively on HTTP 401. OneDrive also
honours refresh-token rotation if Microsoft issues a new one on refresh.

### Licensing

All dependencies are under permissive licenses (MIT / BSD / Apache-2.0 / ISC).
No GPL / LGPL / AGPL — so consumers can link these static libraries into
closed-source binaries without copyleft obligations.

## Usage

### As a Go library

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

Three runnable examples live under [examples/](examples):

- [examples/list](examples/list) — list a remote directory
- [examples/upload](examples/upload) — stream a local file to a remote path
- [examples/walk](examples/walk) — recursive traversal using
  [`pkg/fsutil.Walk`](pkg/fsutil/walk.go)

### As a C library — single driver

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
`onedrive` — same symbol shape.

### As a C library — combined dispatcher

```bash
go build -buildmode=c-archive -o libnetworkfs.a ./cmd/networkfs
```

```c
int  networkfs_mount(int mount_id, int driver_type, const char* config_json);
int  networkfs_unmount(int mount_id);
int  networkfs_stat(int mount_id, const char* path, char** out_json);
int  networkfs_listdir(int mount_id, const char* path, char** out_json);
int  networkfs_openfile(int mount_id, const char* path, ByteSlice* out);
int  networkfs_writefile(int mount_id, const char* path, ByteSlice data);
int  networkfs_mkdir(int mount_id, const char* path);
int  networkfs_remove(int mount_id, const char* path);
int  networkfs_rename(int mount_id, const char* old_path, const char* new_path);
int  networkfs_drivers(char** out_json);
void networkfs_free(char* ptr);
```

`networkfs_mount` return codes: `0` success, `1` unknown driver type,
`2` mount failed, `-1` invalid JSON.

### TUI file browser

A Bubble Tea TUI sits in [cmd/tui](cmd/tui) for interactive smoke-testing:

```bash
make tui
./build/networkfs
```

It enumerates registered drivers, prompts for each driver's config
fields, mounts, and browses. Supports pre-configured accounts via a
`.env.yaml` next to the binary:

```bash
./build/networkfs --account docker-ftp          # interactive browser
./build/networkfs --account docker-ftp /path    # non-interactive listing
```

File previews render text inline and use the Kitty or iTerm2 inline-image
protocols when the terminal advertises support. Detection reads
`KITTY_WINDOW_ID`, `TERM`, and `TERM_PROGRAM`; tmux passthrough is
handled automatically when `TMUX` is set (requires
`set -s allow-passthrough on` in your tmux config).

### Test server

A docker-compose FTP / SFTP / WebDAV / SMB harness with fixture data
lives in [test-server/](test-server). Start it with:

```bash
cd test-server
docker compose up -d
```

Default credentials (`testuser` / `testpass`) are set via build args in
[test-server/docker-compose.yml](test-server/docker-compose.yml) — override with
`TEST_USER=...` / `TEST_PASSWORD=...` env vars.

Four matching account presets in [test-server/.env.yaml](test-server/.env.yaml)
let the TUI connect with no further setup:

```bash
../build/networkfs --account docker-ftp
../build/networkfs --account docker-sftp
../build/networkfs --account docker-webdav
../build/networkfs --account docker-smb
```

Dropbox / GDrive / OneDrive / S3 aren't in the test server (none of
them can be self-hosted in a way that matches their real API surface);
integration tests for those live behind `//go:build <name>_integration`
tags and require real credentials.

## Development

```bash
make test            # go test -race with coverage
make bench           # streaming + list benches vs the embedded FTP server
make archives        # per-driver .a files + libnetworkfs.a in build/
make tui             # TUI binary in build/networkfs
make coverage-html   # open HTML coverage
make vet             # go vet
make tidy            # go mod tidy && go mod verify
```

### Git hooks

One-time setup per clone, so every commit runs `gofmt -s` + `go vet`
(the fast subset of CI) and CI doesn't have to catch what your machine
could have:

```bash
./scripts/install-hooks.sh
```

Bypass a single commit with `git commit --no-verify`.

CI runs tests (with race detector), `go vet`, `gofmt -s`,
`golangci-lint`, `govulncheck`, and all c-archive + TUI builds on every
push and pull request (ubuntu-latest + macos-latest matrix).

## Architecture

```
pkg/api/              - Shared Driver interface + MountManager + errors
pkg/api/cgo/          - Pure-Go cgo helpers (JSON marshal/unmarshal)
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
examples/                        - Runnable Go examples
test-server/                     - docker-compose FTP/SFTP/WebDAV/SMB harness
docs/                            - ROADMAP, DRIVERS, plan notes
```

### Why the cgo helpers are inlined per-main

Each `cmd/*/main.go` carries its own copy of the cgo bridge helpers
(`stringFromC`, `jsonToC`, `setOutBytes`, etc.). When those helpers
live in a separate Go package and their signatures mention `*C.char`,
the `C.char` becomes a package-scoped named type that is not
assignable to `*C.char` in another package's `main`. The
cross-package type identity breaks the build.

`pkg/api/cgo/` therefore exposes only pure-Go helpers (JSON
serialisation, success/error result shapes) that the mains can safely
call. Anything involving `*C.char` — string conversion, byte-slice
pointers, free — has to stay inline in each main.

## Integration with DiskJockey

Vendored as a git submodule:

```bash
cd diskjockey
git submodule add https://github.com/christhomas/go-networkfs.git vendor/go-networkfs
```

The build script [`scripts/build-gonetworkfs.sh`](../../scripts/build-gonetworkfs.sh)
produces all per-driver libs and the combined `libnetworkfs.a` into
`lib/go-networkfs/`. Drivers to build are controlled via the `DRIVERS`
env var (default: `ftp sftp smb dropbox webdav gdrive s3`). Set
`BUILD_COMBINED=0` to skip the combined archive. Set `DJ_GO_DEBUG=1`
to preserve symbols and DWARF info.

## Docs

- [docs/ROADMAP.md](docs/ROADMAP.md) — prioritised plan of what's next
- [docs/DRIVERS.md](docs/DRIVERS.md) — how to write a new driver
- [CHANGELOG.md](CHANGELOG.md) — notable changes

## License

MIT
