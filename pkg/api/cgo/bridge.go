// pkg/api/cgo/bridge.go - pure-Go helpers for cgo driver wrappers.
//
// Helpers never use *C.char in their signatures. In cgo, *C.char is a
// package-local named type (each `import "C"` creates its own), so a
// helper defined in this package cannot accept or return *C.char from
// another package's main. Each driver's cmd/<name>/main.go does its
// own C.CString / C.GoString / C.CBytes / C.free calls and uses these
// helpers for the Go-side JSON / error marshaling.

package cgo

import "encoding/json"

// JSONMarshal serialises v to JSON. On marshal failure it returns an
// {"error":"..."} document instead of panicking, so callers can always
// hand the bytes straight back over the C boundary.
func JSONMarshal(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		errData, _ := json.Marshal(map[string]string{"error": err.Error()})
		return errData
	}
	return data
}

// JSONUnmarshal is a thin wrapper around json.Unmarshal kept here for
// symmetry with JSONMarshal.
func JSONUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// ErrorResult returns a standard error JSON document.
func ErrorResult(code int, message string) []byte {
	return JSONMarshal(map[string]interface{}{
		"success": false,
		"error":   map[string]interface{}{"code": code, "message": message},
	})
}

// SuccessResult returns a standard success JSON document.
func SuccessResult() []byte {
	return []byte(`{"success":true}`)
}
