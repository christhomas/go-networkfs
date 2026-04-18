// ftp/cmd/ftp/main.go - FTP driver C library exports
//
// Builds as: go build -buildmode=c-archive -o libftp.a ./ftp/cmd/ftp
// Exports: ftp_mount, ftp_unmount, ftp_stat, ftp_listdir, ftp_readfile...

package main

/*
#include <stdlib.h>
#include <stdint.h>

typedef struct {
	char* data;
	size_t len;
} ByteSlice;
*/
import "C"

import (
	"unsafe"

	"github.com/christhomas/go-networkfs/ftp"
	"github.com/christhomas/go-networkfs/pkg/api/cgo"
)

var driver = &ftp.FTPDriver{}

//export ftp_version
func ftp_version() *C.char {
	return cgo.StringToC("1.0.0")
}

// mountID: unique identifier for this mount instance
// configJSON: {"host":"...","port":"21","user":"...","pass":"...","root":"/","ftps":"false"}
// Returns: 0 on success, error code on failure
//
//export ftp_mount
func ftp_mount(mountID C.int, configJSON *C.char) C.int {
	config := make(map[string]string)
	if err := cgo.JSONFromC(configJSON, &config); err != nil {
		return -1 // Invalid JSON
	}
	if err := driver.Mount(int(mountID), config); err != nil {
		return 1 // Mount failed
	}
	return 0
}

//export ftp_unmount
func ftp_unmount(mountID C.int) C.int {
	if err := driver.Unmount(int(mountID)); err != nil {
		return 1
	}
	return 0
}

// Returns JSON FileInfo or error (caller must free with ftp_free)
//
//export ftp_stat
func ftp_stat(mountID C.int, path *C.char, outJSON **C.char) C.int {
	info, err := driver.Stat(int(mountID), cgo.StringFromC(path))
	if err != nil {
		cgo.SetOutString(outJSON, cgo.ErrorToC(err))
		return 1
	}
	cgo.SetOutString(outJSON, string(cgo.JSONToC(info)))
	return 0
}

// Returns JSON array of FileInfo (caller must free with ftp_free)
//
//export ftp_listdir
func ftp_listdir(mountID C.int, path *C.char, outJSON **C.char) C.int {
	entries, err := driver.ListDir(int(mountID), cgo.StringFromC(path))
	if err != nil {
		cgo.SetOutString(outJSON, cgo.ErrorToC(err))
		return 1
	}
	cgo.SetOutString(outJSON, string(cgo.JSONToC(entries)))
	return 0
}

// Returns file data in out buffer (caller must free with ftp_free)
//
//export ftp_openfile
func ftp_openfile(mountID C.int, path *C.char, out *C.ByteSlice) C.int {
	reader, err := driver.OpenFile(int(mountID), cgo.StringFromC(path))
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

	cgo.SetOutBytes(&out.data, &out.len, data)
	return 0
}

// data: file contents to write
// Returns: 0 on success
//
//export ftp_writefile
func ftp_writefile(mountID C.int, path *C.char, data C.ByteSlice) C.int {
	writer, err := driver.CreateFile(int(mountID), cgo.StringFromC(path))
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

//export ftp_mkdir
func ftp_mkdir(mountID C.int, path *C.char) C.int {
	if err := driver.Mkdir(int(mountID), cgo.StringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export ftp_remove
func ftp_remove(mountID C.int, path *C.char) C.int {
	if err := driver.Remove(int(mountID), cgo.StringFromC(path)); err != nil {
		return 1
	}
	return 0
}

//export ftp_rename
func ftp_rename(mountID C.int, oldPath *C.char, newPath *C.char) C.int {
	if err := driver.Rename(int(mountID), cgo.StringFromC(oldPath), cgo.StringFromC(newPath)); err != nil {
		return 1
	}
	return 0
}

// Frees memory allocated by ftp_stat, ftp_listdir, ftp_openfile
//
//export ftp_free
func ftp_free(ptr *C.char) {
	cgo.FreeBytes(ptr)
}

func main() {}
