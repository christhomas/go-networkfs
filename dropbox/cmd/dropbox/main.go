// dropbox/cmd/dropbox/main.go - Dropbox driver C library exports
//
// Builds as: go build -buildmode=c-archive -o libdropbox.a ./dropbox/cmd/dropbox
// Exports: dropbox_version, dropbox_mount, dropbox_unmount, dropbox_stat,
// dropbox_listdir, dropbox_openfile, dropbox_writefile, dropbox_mkdir,
// dropbox_remove, dropbox_rename, dropbox_free.
//
// NOTE: cgo marshaling helpers (StringFromC, JSONToC, SetOutString, …)
// are intentionally inlined here rather than imported from
// github.com/christhomas/go-networkfs/pkg/api/cgo. When cgo helpers
// are defined in a separate package, their `C.char` becomes a named
// type scoped to that package — not assignable to `*C.char` in any
// other package's main. The only portable way to share cgo glue is
// to copy it per-binary; that's what we do here.

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

	"github.com/christhomas/go-networkfs/dropbox"
)

var driver = &dropbox.DropboxDriver{}

// --- local cgo marshaling helpers (see NOTE above) -------------------------

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

//export dropbox_version
func dropbox_version() *C.char {
	return stringToC("1.0.0")
}

// mountID: unique identifier for this mount instance
// configJSON: {"access_token":"..."}
// Returns: 0 on success, error code on failure
//
//export dropbox_mount
func dropbox_mount(mountID C.int, configJSON *C.char) C.int {
	config := make(map[string]string)
	if err := jsonFromC(configJSON, &config); err != nil {
		return -1 // Invalid JSON
	}
	if err := driver.Mount(int(mountID), config); err != nil {
		return 1 // Mount failed
	}
	return 0
}

//export dropbox_unmount
func dropbox_unmount(mountID C.int) C.int {
	if err := driver.Unmount(int(mountID)); err != nil {
		return 1
	}
	return 0
}

// Returns JSON FileInfo or error (caller must free with dropbox_free)
//
//export dropbox_stat
func dropbox_stat(mountID C.int, path *C.char, outJSON **C.char) C.int {
	info, err := driver.Stat(int(mountID), stringFromC(path))
	if err != nil {
		setOutString(outJSON, C.GoString(errorToC(err)))
		return 1
	}
	*outJSON = jsonToC(info)
	return 0
}

// Returns JSON array of FileInfo (caller must free with dropbox_free)
//
//export dropbox_listdir
func dropbox_listdir(mountID C.int, path *C.char, outJSON **C.char) C.int {
	entries, err := driver.ListDir(int(mountID), stringFromC(path))
	if err != nil {
		setOutString(outJSON, C.GoString(errorToC(err)))
		return 1
	}
	*outJSON = jsonToC(entries)
	return 0
}

// Returns file data in out buffer (caller must free with dropbox_free)
//
//export dropbox_openfile
func dropbox_openfile(mountID C.int, path *C.char, out *C.ByteSlice) C.int {
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
//export dropbox_writefile
func dropbox_writefile(mountID C.int, path *C.char, data C.ByteSlice) C.int {
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

//export dropbox_mkdir
func dropbox_mkdir(mountID C.int, path *C.char) C.int {
	if err := driver.Mkdir(int(mountID), stringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export dropbox_remove
func dropbox_remove(mountID C.int, path *C.char) C.int {
	if err := driver.Remove(int(mountID), stringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export dropbox_rename
func dropbox_rename(mountID C.int, oldPath *C.char, newPath *C.char) C.int {
	if err := driver.Rename(int(mountID), stringFromC(oldPath), stringFromC(newPath)); err != nil {
		return 1
	}
	return 0
}

// Frees memory allocated by dropbox_stat, dropbox_listdir, dropbox_openfile,
// dropbox_writefile, dropbox_version. Safe to call with NULL.
//
//export dropbox_free
func dropbox_free(ptr *C.char) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

func main() {}
