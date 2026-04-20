// cmd/networkfs/main.go - Unified C library exports for all drivers
//
// Builds as: go build -buildmode=c-archive -o libnetworkfs.a ./cmd/networkfs
// Exports: networkfs_version, networkfs_mount, networkfs_unmount,
// networkfs_stat, networkfs_listdir, networkfs_openfile, networkfs_writefile,
// networkfs_mkdir, networkfs_remove, networkfs_rename, networkfs_free.
//
// Unlike the per-driver libraries (libftp.a, libsftp.a, etc.), this archive
// links every registered driver and dispatches by driver_type at mount time.
// Callers pick the backend by passing driver_type to networkfs_mount:
//   1=FTP, 2=SFTP, 3=SMB, 4=Dropbox, 5=WebDAV, 6=GDrive, 7=S3.
//
// NOTE: cgo marshaling helpers are inlined here for the same reason as the
// per-driver mains — see ftp/cmd/ftp/main.go for the explanation.

package main

/*
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

typedef struct {
	char* data;
	size_t len;
} ByteSlice;
*/
import "C"

import (
	"encoding/json"
	"unsafe"

	"github.com/christhomas/go-networkfs/pkg/api"

	// Blank imports: each driver's init() registers itself with api.registry.
	_ "github.com/christhomas/go-networkfs/dropbox"
	_ "github.com/christhomas/go-networkfs/ftp"
	_ "github.com/christhomas/go-networkfs/gdrive"
	_ "github.com/christhomas/go-networkfs/onedrive"
	_ "github.com/christhomas/go-networkfs/s3"
	_ "github.com/christhomas/go-networkfs/sftp"
	_ "github.com/christhomas/go-networkfs/smb"
	_ "github.com/christhomas/go-networkfs/webdav"
)

var manager = api.NewMountManager()

// --- local cgo marshaling helpers ------------------------------------------

func stringFromC(cstr *C.char) string {
	if cstr == nil {
		return ""
	}
	return C.GoString(cstr)
}

func jsonFromC(cstr *C.char, v interface{}) error {
	return json.Unmarshal([]byte(stringFromC(cstr)), v)
}

func stringToC(s string) *C.char {
	return C.CString(s)
}

func jsonToC(v interface{}) *C.char {
	data, err := json.Marshal(v)
	if err != nil {
		errData, _ := json.Marshal(map[string]string{"error": err.Error()})
		return C.CString(string(errData))
	}
	return C.CString(string(data))
}

func errorToC(err error) *C.char {
	if err == nil {
		return nil
	}
	return C.CString(err.Error())
}

func setOutString(out **C.char, s string) {
	*out = C.CString(s)
}

func setOutBytes(outData **C.char, outLen *C.size_t, data []byte) {
	if len(data) == 0 {
		*outData = nil
		*outLen = 0
		return
	}
	*outData = (*C.char)(C.CBytes(data))
	*outLen = C.size_t(len(data))
}

// --- exports ---------------------------------------------------------------

//export networkfs_version
func networkfs_version() *C.char {
	return stringToC("1.0.0")
}

// mountID: unique identifier for this mount instance
// driverType: 1=FTP, 2=SFTP, 3=SMB, 4=Dropbox, 5=WebDAV, 6=GDrive, 7=S3
// configJSON: driver-specific config, e.g. {"host":"...","user":"...",...}
// Returns: 0 on success, 1 on unknown driver type, 2 on mount failure, -1 on invalid JSON
//
//export networkfs_mount
func networkfs_mount(mountID C.int, driverType C.int, configJSON *C.char) C.int {
	config := make(map[string]string)
	if err := jsonFromC(configJSON, &config); err != nil {
		return -1
	}
	if err := manager.Mount(int(mountID), int(driverType), config); err != nil {
		if err == api.ErrUnknownDriverType {
			return 1
		}
		return 2
	}
	return 0
}

//export networkfs_unmount
func networkfs_unmount(mountID C.int) C.int {
	if err := manager.Unmount(int(mountID)); err != nil {
		return 1
	}
	return 0
}

// Returns JSON FileInfo or error message (caller must free with networkfs_free)
//
//export networkfs_stat
func networkfs_stat(mountID C.int, path *C.char, outJSON **C.char) C.int {
	driver, ok := manager.Get(int(mountID))
	if !ok {
		setOutString(outJSON, "mount not found")
		return 1
	}
	info, err := driver.Stat(int(mountID), stringFromC(path))
	if err != nil {
		setOutString(outJSON, C.GoString(errorToC(err)))
		return 1
	}
	*outJSON = jsonToC(info)
	return 0
}

// Returns JSON array of FileInfo (caller must free with networkfs_free)
//
//export networkfs_listdir
func networkfs_listdir(mountID C.int, path *C.char, outJSON **C.char) C.int {
	driver, ok := manager.Get(int(mountID))
	if !ok {
		setOutString(outJSON, "mount not found")
		return 1
	}
	entries, err := driver.ListDir(int(mountID), stringFromC(path))
	if err != nil {
		setOutString(outJSON, C.GoString(errorToC(err)))
		return 1
	}
	*outJSON = jsonToC(entries)
	return 0
}

// Returns file data in out buffer (caller must free out.data with networkfs_free)
//
//export networkfs_openfile
func networkfs_openfile(mountID C.int, path *C.char, out *C.ByteSlice) C.int {
	driver, ok := manager.Get(int(mountID))
	if !ok {
		return 1
	}
	reader, err := driver.OpenFile(int(mountID), stringFromC(path))
	if err != nil {
		return 1
	}
	defer reader.Close()

	data := make([]byte, 0, 64*1024)
	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	setOutBytes(&out.data, &out.len, data)
	return 0
}

// data: file contents to write
// Returns: 0 on success
//
//export networkfs_writefile
func networkfs_writefile(mountID C.int, path *C.char, data C.ByteSlice) C.int {
	driver, ok := manager.Get(int(mountID))
	if !ok {
		return 1
	}
	writer, err := driver.CreateFile(int(mountID), stringFromC(path))
	if err != nil {
		return 1
	}
	defer writer.Close()

	goData := C.GoBytes(unsafe.Pointer(data.data), C.int(data.len))
	if _, err := writer.Write(goData); err != nil {
		return 1
	}
	return 0
}

//export networkfs_mkdir
func networkfs_mkdir(mountID C.int, path *C.char) C.int {
	driver, ok := manager.Get(int(mountID))
	if !ok {
		return 1
	}
	if err := driver.Mkdir(int(mountID), stringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export networkfs_remove
func networkfs_remove(mountID C.int, path *C.char) C.int {
	driver, ok := manager.Get(int(mountID))
	if !ok {
		return 1
	}
	if err := driver.Remove(int(mountID), stringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export networkfs_rename
func networkfs_rename(mountID C.int, oldPath *C.char, newPath *C.char) C.int {
	driver, ok := manager.Get(int(mountID))
	if !ok {
		return 1
	}
	if err := driver.Rename(int(mountID), stringFromC(oldPath), stringFromC(newPath)); err != nil {
		return 1
	}
	return 0
}

// Returns JSON array of registered driver type IDs (caller must free with networkfs_free)
//
//export networkfs_drivers
func networkfs_drivers(outJSON **C.char) C.int {
	*outJSON = jsonToC(api.ListDriverTypes())
	return 0
}

// Frees memory allocated by the exports above. Safe to call with NULL.
//
//export networkfs_free
func networkfs_free(ptr *C.char) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

func main() {}
