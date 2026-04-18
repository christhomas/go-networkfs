// cmd/nfs/main.go - Unified network filesystem dispatcher
//
// This is the main entry point for go-networkfs. It can be built as:
// 1. CLI tool: go build -o nfs ./cmd/nfs
// 2. C library: go build -buildmode=c-archive -o libnfs.a ./cmd/nfs
//
// The CLI provides commands for mounting and interacting with network
// filesystems. The C library exports functions for integration with
// Swift/Objective-C applications.

package main

/*
#include <stdlib.h>
#include <stdint.h>

// ByteSlice represents a byte buffer for file I/O
typedef struct {
	char* data;
	size_t len;
} ByteSlice;
*/
import "C"

import (
	"fmt"
	"os"

	"github.com/christhomas/go-networkfs/pkg/api"
	"github.com/christhomas/go-networkfs/pkg/api/cgo"

	// Import drivers to register them
	_ "github.com/christhomas/go-networkfs/ftp"
	// _ "github.com/christhomas/go-networkfs/sftp"
	// _ "github.com/christhomas/go-networkfs/smb"
	// _ "github.com/christhomas/go-networkfs/dropbox"
	// _ "github.com/christhomas/go-networkfs/webdav"
)

var mountManager = api.NewMountManager()

// ============================================================================
// C API Exports
// ============================================================================

//export nfs_version
func nfs_version() *C.char {
	return cgo.StringToC("1.0.0")
}

//export nfs_list_drivers
// Returns JSON array of available driver types: [{"id":1,"name":"ftp"},...]
func nfs_list_drivers() *C.char {
	drivers := []map[string]interface{}{}
	for id := range api.ListDriverTypes() {
		d, ok := api.GetDriver(id)
		if ok {
			drivers = append(drivers, map[string]interface{}{
				"id":   id,
				"name": d.Name(),
			})
		}
	}
	return cgo.JSONToC(drivers)
}

//export nfs_mount
// driverType: 1=FTP, 2=SFTP, 3=SMB, 4=Dropbox, 5=WebDAV
// configJSON: {"host":"...","user":"...","pass":"...",...}
// Returns: 0 on success, error code on failure
func nfs_mount(mountID C.int, driverType C.int, configJSON *C.char) C.int {
	config := make(map[string]string)
	if err := cgo.JSONFromC(configJSON, &config); err != nil {
		return -1 // Invalid JSON
	}

	if err := mountManager.Mount(int(mountID), int(driverType), config); err != nil {
		fmt.Fprintf(os.Stderr, "mount error: %v\n", err)
		if driverErr, ok := err.(*api.DriverError); ok {
			return C.int(driverErr.Code)
		}
		return -1
	}
	return 0
}

//export nfs_unmount
func nfs_unmount(mountID C.int) C.int {
	if err := mountManager.Unmount(int(mountID)); err != nil {
		fmt.Fprintf(os.Stderr, "unmount error: %v\n", err)
		return -1
	}
	return 0
}

//export nfs_stat
// Returns JSON FileInfo or null on error (caller must free with FreeBytes)
func nfs_stat(mountID C.int, path *C.char, outJSON **C.char) C.int {
	driver, ok := mountManager.Get(int(mountID))
	if !ok {
		return 2 // Mount not found
	}

	info, err := driver.Stat(int(mountID), cgo.StringFromC(path))
	if err != nil {
		cgo.SetOutString(outJSON, cgo.DriverErrorToJSON(api.ErrNotFound))
		return 1
	}

	cgo.SetOutString(outJSON, string(cgo.JSONToC(info)))
	return 0
}

//export nfs_listdir
// Returns JSON array of FileInfo or null on error
func nfs_listdir(mountID C.int, path *C.char, outJSON **C.char) C.int {
	driver, ok := mountManager.Get(int(mountID))
	if !ok {
		return 2
	}

	entries, err := driver.ListDir(int(mountID), cgo.StringFromC(path))
	if err != nil {
		return 1
	}

	cgo.SetOutString(outJSON, string(cgo.JSONToC(entries)))
	return 0
}

//export nfs_readfile
// Reads entire file into allocated buffer (caller must free with FreeBytes)
func nfs_readfile(mountID C.int, path *C.char, out *C.ByteSlice) C.int {
	driver, ok := mountManager.Get(int(mountID))
	if !ok {
		return 2
	}

	reader, err := driver.OpenFile(int(mountID), cgo.StringFromC(path))
	if err != nil {
		return 1
	}
	defer reader.Close()

	// Read all data
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

//export nfs_free
// Frees memory allocated by nfs_stat, nfs_listdir, nfs_readfile
func nfs_free(ptr *C.char) {
	cgo.FreeBytes(ptr)
}

// ============================================================================
// CLI Main (only runs when built as executable, not as C library)
// ============================================================================

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "version":
		fmt.Println("go-networkfs v1.0.0")
	case "drivers":
		listDrivers()
	case "mount":
		cliMount()
	case "unmount":
		cliUnmount()
	case "ls":
		cliList()
	case "cat":
		cliCat()
	case "stat":
		cliStat()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: nfs <command> [args]

Commands:
  version              Show version
  drivers              List available driver types
  mount <type> <id>    Mount a filesystem (interactive config)
  unmount <id>         Unmount by ID
  ls <id> <path>       List directory contents
  cat <id> <path>      Read and output file contents
  stat <id> <path>     Show file info

Driver types:
  1 = FTP
  2 = SFTP (coming soon)
  3 = SMB (coming soon)
  4 = Dropbox (coming soon)
  5 = WebDAV (coming soon)

Examples:
  nfs drivers
  nfs mount 1 100                          # Mount FTP as ID 100
  nfs ls 100 /                             # List root
  nfs cat 100 /readme.txt                  # Read file
  nfs unmount 100                          # Unmount
`)
}

func listDrivers() {
	types := api.ListDriverTypes()
	if len(types) == 0 {
		fmt.Println("No drivers registered")
		return
	}
	fmt.Println("Available drivers:")
	for _, id := range types {
		if d, ok := api.GetDriver(id); ok {
			fmt.Printf("  %d = %s\n", id, d.Name())
		}
	}
}

func cliMount() {
	// TODO: Interactive mount with flag parsing
	fmt.Println("Interactive mount not yet implemented")
	fmt.Println("Use C API for now: nfs_mount(id, type, configJSON)")
}

func cliUnmount() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: nfs unmount <mount-id>")
		os.Exit(1)
	}
	// TODO: Parse mount ID and unmount
}

func cliList() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: nfs ls <mount-id> <path>")
		os.Exit(1)
	}
	// TODO: List directory
}

func cliCat() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: nfs cat <mount-id> <path>")
		os.Exit(1)
	}
	// TODO: Read and output file
}

func cliStat() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: nfs stat <mount-id> <path>")
		os.Exit(1)
	}
	// TODO: Show file stats
}
