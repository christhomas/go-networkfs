// pkg/api/cgo/bridge.go - C bridge utilities for go-networkfs
//
// This package provides helper functions for marshaling data between
// C and Go in the cgo exported functions. It handles common patterns
// like JSON serialization, error handling, and memory management.

package cgo

/*
#include <stdlib.h>
#include <string.h>
*/
import "C"

import (
	"encoding/json"
	"unsafe"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// StringToC converts a Go string to a C string (caller must free with C.free)
func StringToC(s string) *C.char {
	return C.CString(s)
}

// StringFromC converts a C string to a Go string
func StringFromC(cstr *C.char) string {
	if cstr == nil {
		return ""
	}
	return C.GoString(cstr)
}

// JSONToC marshals a Go value to JSON and returns as C string
// Caller must free the returned pointer with C.free
func JSONToC(v interface{}) *C.char {
	data, err := json.Marshal(v)
	if err != nil {
		// Return error as JSON
		errData, _ := json.Marshal(map[string]string{"error": err.Error()})
		return C.CString(string(errData))
	}
	return C.CString(string(data))
}

// JSONFromC unmarshals a C string (JSON) into a Go value
func JSONFromC(cstr *C.char, v interface{}) error {
	str := C.GoString(cstr)
	return json.Unmarshal([]byte(str), v)
}

// ErrorToC converts a Go error to a C string representation
// Returns NULL if err is nil
func ErrorToC(err error) *C.char {
	if err == nil {
		return nil
	}
	return C.CString(err.Error())
}

// DriverErrorToJSON converts a DriverError to JSON C string
func DriverErrorToJSON(err *api.DriverError) *C.char {
	if err == nil {
		return C.CString("null")
	}
	return JSONToC(err)
}

// SetOutString sets a C string output parameter (for returning strings to C)
func SetOutString(out **C.char, s string) {
	*out = C.CString(s)
}

// SetOutBytes sets a C byte buffer output parameter
// Caller must free the returned data with C.free
func SetOutBytes(outData **C.char, outLen *C.size_t, data []byte) {
	if len(data) == 0 {
		*outData = nil
		*outLen = 0
		return
	}
	// Allocate C memory and copy data
	cBytes := C.CBytes(data)
	*outData = (*C.char)(cBytes)
	*outLen = C.size_t(len(data))
}

// FreeBytes releases memory allocated by SetOutBytes
//export FreeBytes
func FreeBytes(data *C.char) {
	C.free(unsafe.Pointer(data))
}

// SuccessResult returns a standard success JSON
func SuccessResult() *C.char {
	return C.CString(`{"success":true}`)
}

// ErrorResult returns a standard error JSON
func ErrorResult(code int, message string) *C.char {
	err := map[string]interface{}{
		"success": false,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	return JSONToC(err)
}
