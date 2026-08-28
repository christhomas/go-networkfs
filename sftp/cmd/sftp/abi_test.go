package main

// Tests for the C ABI this archive exposes.
//
// These functions are the whole public surface of libsftp.a: everything a
// consumer touches, it touches through them. They marshal across the cgo
// boundary by hand, and a mistake there is a wrong pointer or a leak rather
// than a compile error, so the round trips are worth pinning.
//
// A test file may not `import "C"` — Go rejects that outright — so the C types
// are reached by inference from the package's own helpers rather than named.
// That covers everything except the three entry points taking a ByteSlice or a
// size_t, which cannot be constructed without naming the type: setOutBytes,
// sftp_openfile and sftp_writefile are left to the driver's own tests.
//
// The mutating exports are exercised in their not-mounted state. That is the
// path a consumer hits first, and the one that must return a code rather than
// panic on a driver that was never mounted.

import (
	"encoding/json"
	"testing"
)

func TestStringRoundTrip(t *testing.T) {
	for _, s := range []string{"", "plain", "with spaces", "ünïcødé", `{"json":"ish"}`} {
		c := stringToC(s)
		got := stringFromC(c)
		sftp_free(c)
		if got != s {
			t.Errorf("round trip of %q gave %q", s, got)
		}
	}
}

func TestStringFromNilIsEmpty(t *testing.T) {
	if got := stringFromC(nil); got != "" {
		t.Errorf("stringFromC(nil) = %q, want empty", got)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	in := map[string]string{"host": "example", "port": "21"}

	c := jsonToC(in)
	defer sftp_free(c)

	var out map[string]string
	if err := jsonFromC(c, &out); err != nil {
		t.Fatalf("jsonFromC: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d keys, want %d", len(out), len(in))
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("key %s = %q, want %q", k, out[k], v)
		}
	}
}

// A value that cannot be marshalled must still produce a readable C string
// rather than a nil the caller will dereference.
func TestJSONToCReportsMarshalFailure(t *testing.T) {
	c := jsonToC(make(chan int))
	if c == nil {
		t.Fatal("jsonToC returned nil for an unmarshallable value")
	}
	defer sftp_free(c)

	var out map[string]string
	if err := json.Unmarshal([]byte(stringFromC(c)), &out); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if out["error"] == "" {
		t.Error("result carries no error message")
	}
}

func TestJSONFromCRejectsGarbage(t *testing.T) {
	c := stringToC("not json at all")
	defer sftp_free(c)

	var out map[string]string
	if err := jsonFromC(c, &out); err == nil {
		t.Error("jsonFromC accepted garbage")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestErrorToC(t *testing.T) {
	if got := errorToC(nil); got != nil {
		sftp_free(got)
		t.Error("errorToC(nil) should be nil, so the caller can test for it")
	}

	c := errorToC(errString("broken"))
	if c == nil {
		t.Fatal("errorToC returned nil for a real error")
	}
	defer sftp_free(c)
	if got := stringFromC(c); got != "broken" {
		t.Errorf("errorToC gave %q, want broken", got)
	}
}

func TestSetOutString(t *testing.T) {
	out := stringToC("")
	sftp_free(out)

	setOutString(&out, "value")
	if out == nil {
		t.Fatal("setOutString left the out pointer nil")
	}
	defer sftp_free(out)
	if got := stringFromC(out); got != "value" {
		t.Errorf("out = %q, want value", got)
	}
}

func TestVersionIsReported(t *testing.T) {
	c := sftp_version()
	if c == nil {
		t.Fatal("version returned nil")
	}
	defer sftp_free(c)
	if stringFromC(c) == "" {
		t.Error("version is empty")
	}
}

func TestFreeToleratesNil(t *testing.T) {
	sftp_free(nil) // must not crash
}

// Invalid JSON is rejected before the driver is asked to do anything.
func TestMountRejectsInvalidJSON(t *testing.T) {
	bad := stringToC("{not json")
	defer sftp_free(bad)

	if got := sftp_mount(1, bad); int(got) != -1 {
		t.Errorf("mount with bad JSON returned %d, want -1", int(got))
	}
}

// An empty config is well-formed JSON but missing everything required, so the
// driver must refuse it rather than half-mount.
func TestMountRejectsEmptyConfig(t *testing.T) {
	empty := stringToC("{}")
	defer sftp_free(empty)

	if got := sftp_mount(1, empty); int(got) == 0 {
		_ = sftp_unmount(1)
		t.Error("mount succeeded with an empty config")
	}
}

// Every operation on an unmounted driver returns non-zero rather than
// panicking. This is the state a consumer is in before the first mount, and
// after a failed one.
func TestOperationsOnUnmountedDriver(t *testing.T) {
	path := stringToC("/some/path")
	defer sftp_free(path)
	other := stringToC("/other/path")
	defer sftp_free(other)

	t.Run("stat", func(t *testing.T) {
		out := stringToC("")
		sftp_free(out)
		if rc := sftp_stat(99, path, &out); int(rc) == 0 {
			t.Error("stat succeeded while unmounted")
		}
	})

	t.Run("listdir", func(t *testing.T) {
		out := stringToC("")
		sftp_free(out)
		if rc := sftp_listdir(99, path, &out); int(rc) == 0 {
			t.Error("listdir succeeded while unmounted")
		}
	})

	t.Run("mkdir", func(t *testing.T) {
		if rc := sftp_mkdir(99, path); int(rc) == 0 {
			t.Error("mkdir succeeded while unmounted")
		}
	})

	t.Run("remove", func(t *testing.T) {
		if rc := sftp_remove(99, path); int(rc) == 0 {
			t.Error("remove succeeded while unmounted")
		}
	})

	t.Run("rename", func(t *testing.T) {
		if rc := sftp_rename(99, path, other); int(rc) == 0 {
			t.Error("rename succeeded while unmounted")
		}
	})

	// Unmount is idempotent by design, so unlike the others it succeeds:
	// tearing down what was never set up is not an error.
	t.Run("unmount is idempotent", func(t *testing.T) {
		if rc := sftp_unmount(99); int(rc) != 0 {
			t.Errorf("unmount of a never-mounted driver returned %d, want 0", int(rc))
		}
	})
}
