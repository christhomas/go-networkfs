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

| Type    | ID | Package                  | Upstream dependency                             |
|---------|----|--------------------------|-------------------------------------------------|
| FTP     |  1 | `go-networkfs/ftp`       | `github.com/jlaffaye/ftp`                       |
| SFTP    |  2 | `go-networkfs/sftp`      | `github.com/pkg/sftp` + `golang.org/x/crypto`   |
| SMB     |  3 | `go-networkfs/smb`       | `github.com/hirochachacha/go-smb2`              |
| Dropbox |  4 | `go-networkfs/dropbox`   | `github.com/dropbox/dropbox-sdk-go-unofficial/v6` |
| WebDAV  |  5 | `go-networkfs/webdav`    | `github.com/studio-b12/gowebdav`                |
| GDrive  |  6 | `go-networkfs/gdrive`    | net/http (Drive v3 REST, OAuth2 refresh)        |
| S3      |  7 | `go-networkfs/s3`        | `github.com/minio/minio-go/v7`                  |

All drivers implement: `Mount`, `Unmount`, `Stat`, `ListDir`, `OpenFile`,
`CreateFile`, `Mkdir`, `Remove`, `Rename`.

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

Swap `ftp` for `sftp`, `smb`, `dropbox`, `webdav`, `gdrive`, or `s3` — same symbol shape.

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

## Architecture

```
pkg/api/            - Shared Driver interface + MountManager
pkg/api/cgo/        - C bridge utilities (reference; inlined per-main)
cmd/networkfs/      - Combined dispatcher   -> libnetworkfs.a
ftp/     ftp/cmd/ftp/     - FTP driver      -> libftp.a
sftp/    sftp/cmd/sftp/   - SFTP driver     -> libsftp.a
smb/     smb/cmd/smb/     - SMB driver      -> libsmb.a
dropbox/ dropbox/cmd/dropbox/ - Dropbox driver -> libdropbox.a
webdav/  webdav/cmd/webdav/   - WebDAV driver  -> libwebdav.a
gdrive/  gdrive/cmd/gdrive/   - GDrive driver  -> libgdrive.a
s3/      s3/cmd/s3/           - S3 driver      -> libs3.a
```

### Why the cgo helpers are inlined per-main

Each `cmd/*/main.go` carries its own copy of `stringFromC`, `jsonToC`,
`setOutBytes`, etc. When those helpers live in a separate Go package,
their `C.char` becomes a package-scoped named type that is not
assignable to `*C.char` in another package's main. The only portable
way to share cgo glue across c-archive binaries is to copy it —
`pkg/api/cgo/` exists as reference but is not imported by the mains.

## Integration with DiskJockey

Vendored as a git submodule:

```bash
cd diskjockey
git submodule add https://github.com/christhomas/go-networkfs.git vendor/go-networkfs
```

The build script [`scripts/build-gonetworkfs.sh`](../../scripts/build-gonetworkfs.sh)
produces all per-driver libs and the combined `libnetworkfs.a` into
`lib/go-networkfs/`. Drivers to build are controlled via the `DRIVERS`
env var (default: `ftp sftp smb dropbox webdav gdrive s3`). Set `BUILD_COMBINED=0`
to skip the combined archive. Set `DJ_GO_DEBUG=1` to preserve symbols
and DWARF info.

## License

MIT
