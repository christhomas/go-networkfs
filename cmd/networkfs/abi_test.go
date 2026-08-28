package main

// Tests for the unified C ABI.
//
// This archive differs from the per-driver ones: it links every driver and
// dispatches on driver_type at mount time, so it has real behaviour of its own
// — a mount manager keyed by mount id, and three distinct failure codes that a
// caller is expected to tell apart.
//
// A test file may not `import "C"`, so the C types are reached by inference
// from the package's own helpers. That leaves setOutBytes, networkfs_openfile
// and networkfs_writefile uncovered here, since neither a ByteSlice nor a
// size_t can be constructed without naming the type.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/christhomas/go-networkfs/pkg/api"
)

func TestStringRoundTrip(t *testing.T) {
	for _, s := range []string{"", "plain", "ünïcødé", `{"json":"ish"}`} {
		c := stringToC(s)
		got := stringFromC(c)
		networkfs_free(c)
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
	defer networkfs_free(c)

	var out map[string]string
	if err := jsonFromC(c, &out); err != nil {
		t.Fatalf("jsonFromC: %v", err)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("key %s = %q, want %q", k, out[k], v)
		}
	}
}

func TestJSONToCReportsMarshalFailure(t *testing.T) {
	c := jsonToC(make(chan int))
	if c == nil {
		t.Fatal("jsonToC returned nil for an unmarshallable value")
	}
	defer networkfs_free(c)

	var out map[string]string
	if err := json.Unmarshal([]byte(stringFromC(c)), &out); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if out["error"] == "" {
		t.Error("result carries no error message")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestErrorToC(t *testing.T) {
	if got := errorToC(nil); got != nil {
		networkfs_free(got)
		t.Error("errorToC(nil) should be nil")
	}
	c := errorToC(errString("broken"))
	defer networkfs_free(c)
	if got := stringFromC(c); got != "broken" {
		t.Errorf("errorToC gave %q, want broken", got)
	}
}

func TestSetOutString(t *testing.T) {
	out := stringToC("")
	networkfs_free(out)

	setOutString(&out, "value")
	if out == nil {
		t.Fatal("setOutString left the pointer nil")
	}
	defer networkfs_free(out)
	if got := stringFromC(out); got != "value" {
		t.Errorf("out = %q, want value", got)
	}
}

func TestVersionIsReported(t *testing.T) {
	c := networkfs_version()
	defer networkfs_free(c)
	if stringFromC(c) == "" {
		t.Error("version is empty")
	}
}

func TestFreeToleratesNil(t *testing.T) {
	networkfs_free(nil)
}

// The whole point of this archive is that every driver is linked in, so the
// registry must report all of them.
func TestDriversListsEveryRegisteredDriver(t *testing.T) {
	out := stringToC("")
	networkfs_free(out)

	if rc := networkfs_drivers(&out); int(rc) != 0 {
		t.Fatalf("networkfs_drivers returned %d", int(rc))
	}
	defer networkfs_free(out)

	var types []int
	if err := json.Unmarshal([]byte(stringFromC(out)), &types); err != nil {
		t.Fatalf("driver list is not JSON: %v (%s)", err, stringFromC(out))
	}

	registered := api.ListDriverTypes()
	if len(types) != len(registered) {
		t.Errorf("reported %d drivers, registry holds %d", len(types), len(registered))
	}
	if len(types) < 8 {
		t.Errorf("only %d drivers linked, expected all eight: %v", len(types), types)
	}
}

// The three mount failures are distinguished by return code, and a caller is
// expected to tell them apart, so each is pinned.
//
// The driver type is written as a literal in each case rather than pulled from
// a table: it is a C.int, and only an untyped constant converts to one without
// naming the type, which a test file cannot do.
// checkMount takes the message already read out, so the helper never has to
// name a cgo type.
func checkMount(t *testing.T, rc, wantCode int, msg, wantErr string) {
	t.Helper()
	if rc != wantCode {
		t.Errorf("mount returned %d, want %d", rc, wantCode)
	}
	if msg == "" {
		t.Fatal("no error message written")
	}
	if wantErr != "" && !strings.Contains(msg, wantErr) {
		t.Errorf("error %q does not mention %q", msg, wantErr)
	}
}

func TestMountRejectsInvalidJSON(t *testing.T) {
	cfg := stringToC("{not json")
	defer networkfs_free(cfg)

	outErr := stringToC("")
	networkfs_free(outErr)

	rc := networkfs_mount(77, 3, cfg, &outErr)
	defer networkfs_free(outErr)
	checkMount(t, int(rc), -1, stringFromC(outErr), "invalid config JSON")
}

func TestMountRejectsUnknownDriverType(t *testing.T) {
	cfg := stringToC("{}")
	defer networkfs_free(cfg)

	outErr := stringToC("")
	networkfs_free(outErr)

	rc := networkfs_mount(77, 999, cfg, &outErr)
	defer networkfs_free(outErr)
	checkMount(t, int(rc), 1, stringFromC(outErr), "unknown driver type")
}

func TestMountReportsDriverFailure(t *testing.T) {
	cfg := stringToC(`{"host":""}`)
	defer networkfs_free(cfg)

	outErr := stringToC("")
	networkfs_free(outErr)

	rc := networkfs_mount(77, 3, cfg, &outErr)
	defer networkfs_free(outErr)
	checkMount(t, int(rc), 2, stringFromC(outErr), "")
}

// Every operation against a mount id that was never mounted must report it
// rather than dereferencing a driver that is not there.
func TestOperationsOnUnknownMount(t *testing.T) {
	const unknown = 4242

	path := stringToC("/some/path")
	defer networkfs_free(path)
	other := stringToC("/other/path")
	defer networkfs_free(other)

	t.Run("stat", func(t *testing.T) {
		out := stringToC("")
		networkfs_free(out)
		if rc := networkfs_stat(unknown, path, &out); int(rc) == 0 {
			t.Error("stat succeeded on an unknown mount")
		}
		if out != nil {
			defer networkfs_free(out)
			if !strings.Contains(stringFromC(out), "mount not found") {
				t.Errorf("message %q does not say the mount is missing", stringFromC(out))
			}
		}
	})

	t.Run("listdir", func(t *testing.T) {
		out := stringToC("")
		networkfs_free(out)
		if rc := networkfs_listdir(unknown, path, &out); int(rc) == 0 {
			t.Error("listdir succeeded on an unknown mount")
		}
		if out != nil {
			networkfs_free(out)
		}
	})

	t.Run("mkdir", func(t *testing.T) {
		if rc := networkfs_mkdir(unknown, path); int(rc) == 0 {
			t.Error("mkdir succeeded on an unknown mount")
		}
	})

	t.Run("remove", func(t *testing.T) {
		if rc := networkfs_remove(unknown, path); int(rc) == 0 {
			t.Error("remove succeeded on an unknown mount")
		}
	})

	t.Run("rename", func(t *testing.T) {
		if rc := networkfs_rename(unknown, path, other); int(rc) == 0 {
			t.Error("rename succeeded on an unknown mount")
		}
	})

	t.Run("unmount", func(t *testing.T) {
		if rc := networkfs_unmount(unknown); int(rc) == 0 {
			t.Error("unmount succeeded on an unknown mount")
		}
	})
}
