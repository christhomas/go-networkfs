# go-networkfs

Network filesystem drivers for Go, with C library exports for Swift/Objective-C integration.

## Drivers

| Type | ID | Package | Status |
|------|-----|---------|--------|
| FTP | 1 | `go-networkfs/ftp` | Skeleton |
| SFTP | 2 | `go-networkfs/sftp` | Planned |
| SMB | 3 | `go-networkfs/smb` | Planned |
| Dropbox | 4 | `go-networkfs/dropbox` | Planned |
| WebDAV | 5 | `go-networkfs/webdav` | Planned |

## Usage

### As a Go library

```go
import (
    "github.com/christhomas/go-networkfs/pkg/api"
    _ "github.com/christhomas/go-networkfs/ftp" // Register driver
)

// Mount FTP
driver, _ := api.GetDriver(1) // 1 = FTP
driver.Mount(100, map[string]string{
    "host": "ftp.example.com",
    "user": "admin",
    "pass": "secret",
})

// Use filesystem
info, _ := driver.Stat(100, "/readme.txt")
entries, _ := driver.ListDir(100, "/")
```

### As a C library

Build the static library:

```bash
go build -buildmode=c-archive -o libnfs.a ./cmd/nfs
```

Link in your C/Swift project and use the exported functions:

```c
int nfs_mount(int mount_id, int driver_type, const char* config_json);
int nfs_unmount(int mount_id);
int nfs_stat(int mount_id, const char* path, char** out_json);
void nfs_free(char* ptr);
```

### CLI tool

```bash
go build -o nfs ./cmd/nfs

./nfs version
./nfs drivers
./nfs mount 1 100  # Type 1 (FTP), ID 100
```

## Architecture

```
cmd/nfs/          - Unified dispatcher (C exports + CLI)
pkg/api/          - Shared Driver interface
pkg/api/cgo/      - C bridge utilities
ftp/              - FTP driver implementation
sftp/             - SFTP driver implementation (planned)
smb/              - SMB driver implementation (planned)
dropbox/          - Dropbox driver implementation (planned)
webdav/           - WebDAV driver implementation (planned)
```

## Integration with DiskJockey

This library is vendored into the DiskJockey project:

```bash
cd diskjockey
git submodule add https://github.com/christhomas/go-networkfs.git vendor/go-networkfs
```

The build system automatically compiles it as a static library:
- `make vendor-gonetworkfs` - Build the library
- Xcode build phase calls `scripts/build-gonetworkfs.sh`

## License

MIT
