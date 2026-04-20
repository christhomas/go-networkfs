package api

import "testing"

// These tests pin down that both `DriverError{...}` (value) and
// `&DriverError{...}` (pointer) satisfy the error interface. With a
// pointer-receiver Error(), only the pointer form satisfies error —
// the value form silently compiles at the call site but doesn't
// implement the interface, which used to cause "cannot use X as error"
// diagnostics only much later during usage.

func TestDriverErrorValueSatisfiesErrorInterface(t *testing.T) {
	// Compile-time: if Error() has a pointer receiver this line fails
	// to compile rather than running the test.
	var e error = DriverError{Code: 1, Message: "value"}
	if e.Error() != "value" {
		t.Fatalf("value receiver Error() = %q, want %q", e.Error(), "value")
	}
}

func TestDriverErrorPointerSatisfiesErrorInterface(t *testing.T) {
	var e error = &DriverError{Code: 2, Message: "pointer"}
	if e.Error() != "pointer" {
		t.Fatalf("pointer receiver Error() = %q, want %q", e.Error(), "pointer")
	}
}
