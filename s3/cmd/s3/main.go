// s3/cmd/s3/main.go - S3 driver C library exports
//
// Builds as: go build -buildmode=c-archive -o libs3.a ./s3/cmd/s3
// Exports: s3_version, s3_mount, s3_unmount, s3_stat, s3_listdir,
// s3_openfile, s3_writefile, s3_mkdir, s3_remove, s3_rename, s3_free.
//
// NOTE: cgo marshaling helpers are intentionally inlined here rather
// than imported from pkg/api/cgo. See dropbox/cmd/dropbox for the
// rationale.

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

	"github.com/christhomas/go-networkfs/s3"
)

var driver = &s3.S3Driver{}

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

//export s3_version
func s3_version() *C.char {
	return stringToC("1.0.0")
}

// mountID: unique identifier for this mount instance
// configJSON: {"endpoint":"...","bucket":"...","access_key_id":"...","secret_access_key":"...", ...}
// Returns: 0 on success, error code on failure
//
//export s3_mount
func s3_mount(mountID C.int, configJSON *C.char) C.int {
	config := make(map[string]string)
	if err := jsonFromC(configJSON, &config); err != nil {
		return -1
	}
	if err := driver.Mount(int(mountID), config); err != nil {
		return 1
	}
	return 0
}

//export s3_unmount
func s3_unmount(mountID C.int) C.int {
	if err := driver.Unmount(int(mountID)); err != nil {
		return 1
	}
	return 0
}

// Returns JSON FileInfo or error (caller must free with s3_free)
//
//export s3_stat
func s3_stat(mountID C.int, path *C.char, outJSON **C.char) C.int {
	info, err := driver.Stat(int(mountID), stringFromC(path))
	if err != nil {
		setOutString(outJSON, C.GoString(errorToC(err)))
		return 1
	}
	*outJSON = jsonToC(info)
	return 0
}

// Returns JSON array of FileInfo (caller must free with s3_free)
//
//export s3_listdir
func s3_listdir(mountID C.int, path *C.char, outJSON **C.char) C.int {
	entries, err := driver.ListDir(int(mountID), stringFromC(path))
	if err != nil {
		setOutString(outJSON, C.GoString(errorToC(err)))
		return 1
	}
	*outJSON = jsonToC(entries)
	return 0
}

// Returns file data in out buffer (caller must free with s3_free)
//
//export s3_openfile
func s3_openfile(mountID C.int, path *C.char, out *C.ByteSlice) C.int {
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
//export s3_writefile
func s3_writefile(mountID C.int, path *C.char, data C.ByteSlice) C.int {
	writer, err := driver.CreateFile(int(mountID), stringFromC(path))
	if err != nil {
		return 1
	}
	goData := C.GoBytes(unsafe.Pointer(data.data), C.int(data.len))
	if _, err := writer.Write(goData); err != nil {
		writer.Close()
		return 1
	}
	if err := writer.Close(); err != nil {
		return 1
	}
	return 0
}

//export s3_mkdir
func s3_mkdir(mountID C.int, path *C.char) C.int {
	if err := driver.Mkdir(int(mountID), stringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export s3_remove
func s3_remove(mountID C.int, path *C.char) C.int {
	if err := driver.Remove(int(mountID), stringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export s3_rename
func s3_rename(mountID C.int, oldPath *C.char, newPath *C.char) C.int {
	if err := driver.Rename(int(mountID), stringFromC(oldPath), stringFromC(newPath)); err != nil {
		return 1
	}
	return 0
}

// Frees memory allocated by s3_stat, s3_listdir, s3_openfile,
// s3_writefile, s3_version. Safe to call with NULL.
//
//export s3_free
func s3_free(ptr *C.char) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

func main() {}
