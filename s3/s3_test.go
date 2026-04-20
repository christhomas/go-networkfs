// s3/s3_test.go - Unit tests for pure helpers and driver surface.
//
// These tests are hermetic: no S3/MinIO server is required. They cover
// the path-normalisation helpers, the driver's identity/registration,
// and the not-connected guards on every Driver-interface method.

package s3

import (
	"errors"
	"testing"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// normalizePrefix: "" stays "", otherwise trimmed of leading/trailing
// slashes and then a single trailing "/" is appended.
func TestNormalizePrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", ""},
		{"//", ""},
		{"///", ""},
		{"foo", "foo/"},
		{"foo/", "foo/"},
		{"/foo", "foo/"},
		{"/foo/", "foo/"},
		{"foo/bar", "foo/bar/"},
		{"/foo/bar/", "foo/bar/"},
		{"//foo//bar//", "foo//bar/"}, // only outermost slashes are trimmed
		{"a", "a/"},
		{"café", "café/"},     // unicode preserved
		{"日本語/", "日本語/"},     // unicode preserved
		{"/日本語", "日本語/"},
	}
	for _, c := range cases {
		if got := normalizePrefix(c.in); got != c.want {
			t.Errorf("normalizePrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// normPath canonicalises: "" and "/" -> "/"; otherwise ensure leading
// slash and trim trailing slashes.
func TestNormPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/foo", "/foo"},
		{"/foo/", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"/foo/bar/", "/foo/bar"},
		{"/foo/bar///", "/foo/bar"},
		{"foo", "/foo"},
		{"foo/", "/foo"},
		{"foo/bar", "/foo/bar"},
		{"foo/bar/", "/foo/bar"},
		{"/a/b/c.txt", "/a/b/c.txt"},
		{"/日本語", "/日本語"},
		{"日本語/café", "/日本語/café"},
	}
	for _, c := range cases {
		if got := normPath(c.in); got != c.want {
			t.Errorf("normPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// nameFromPath returns the right-most non-empty path segment.
func TestNameFromPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", ""},
		{"//", ""},
		{"///", ""},
		{"/foo", "foo"},
		{"/foo/", "foo"},
		{"/foo/bar", "bar"},
		{"/foo/bar/", "bar"},
		{"/foo/bar//", "bar"},
		{"foo", "foo"},
		{"foo/bar", "bar"},
		{"/a/b/c.txt", "c.txt"},
		{"/日本語/café.txt", "café.txt"},
		{"/ space ", " space "}, // surrounding spaces preserved; only "/" separates segments
	}
	for _, c := range cases {
		if got := nameFromPath(c.in); got != c.want {
			t.Errorf("nameFromPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// toKey: strips the single leading "/" from its argument and prepends
// d.prefix. When d.prefix is "" the result is just the path without
// leading slash; when d.prefix is non-empty it's assumed to already
// include its trailing slash (that's normalizePrefix's invariant).
func TestToKey(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		path   string
		want   string
	}{
		// Empty prefix.
		{"empty prefix, root", "", "/", ""},
		{"empty prefix, empty path", "", "", ""},
		{"empty prefix, /foo", "", "/foo", "foo"},
		{"empty prefix, /foo/bar", "", "/foo/bar", "foo/bar"},
		{"empty prefix, foo (no leading slash)", "", "foo", "foo"},
		{"empty prefix, foo/bar", "", "foo/bar", "foo/bar"},
		{"empty prefix, /a/b/c.txt", "", "/a/b/c.txt", "a/b/c.txt"},
		{"empty prefix, unicode", "", "/日本語/café", "日本語/café"},

		// Normalised (trailing-slash) prefix — the canonical shape the
		// driver itself stores after Mount().
		{"prefix foo/, root", "foo/", "/", "foo/"},
		{"prefix foo/, /bar", "foo/", "/bar", "foo/bar"},
		{"prefix foo/, /bar/baz", "foo/", "/bar/baz", "foo/bar/baz"},
		{"prefix foo/, bar (no leading slash)", "foo/", "bar", "foo/bar"},
		{"prefix a/b/, /x/y", "a/b/", "/x/y", "a/b/x/y"},
		{"prefix unicode/, /file", "日本語/", "/file", "日本語/file"},

		// Non-canonical prefix WITHOUT trailing slash. toKey does not
		// inject a separator; the test pins the observed behaviour so a
		// change is a deliberate decision.
		{"prefix foo (no slash), /bar", "foo", "/bar", "foobar"},
		{"prefix foo (no slash), root", "foo", "/", "foo"},
		{"prefix foo (no slash), bar", "foo", "bar", "foobar"},

		// toKey only trims ONE leading slash.
		{"empty prefix, //foo", "", "//foo", "/foo"},
		{"prefix foo/, //bar", "foo/", "//bar", "foo//bar"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &S3Driver{prefix: c.prefix}
			if got := d.toKey(c.path); got != c.want {
				t.Errorf("(&S3Driver{prefix:%q}).toKey(%q) = %q, want %q",
					c.prefix, c.path, got, c.want)
			}
		})
	}
}

// --- driver surface --------------------------------------------------------

func TestDriverTypeID(t *testing.T) {
	if DriverTypeID != 7 {
		t.Fatalf("DriverTypeID = %d, want 7", DriverTypeID)
	}
}

func TestName(t *testing.T) {
	d := &S3Driver{}
	if got := d.Name(); got != "s3" {
		t.Fatalf("Name() = %q, want %q", got, "s3")
	}
}

func TestRegisteredInGlobalRegistry(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("driver type %d not registered", DriverTypeID)
	}
	if d.Name() != "s3" {
		t.Fatalf("registry returned %q, want %q", d.Name(), "s3")
	}
	if _, ok := d.(*S3Driver); !ok {
		t.Fatalf("registry returned wrong concrete type: %T", d)
	}
}

// Mount() requires endpoint/bucket/access_key_id/secret_access_key and
// returns a *DriverError with Code 10 otherwise.
func TestMountMissingRequiredConfig(t *testing.T) {
	d := &S3Driver{}

	if err := d.Mount(1, nil); err == nil {
		t.Error("nil config should error")
	}
	if err := d.Mount(1, map[string]string{}); err == nil {
		t.Error("empty config should error")
	}

	// Partial configs — each missing field should still be rejected.
	partial := []map[string]string{
		{"endpoint": "s3.example.com"},
		{"endpoint": "s3.example.com", "bucket": "b"},
		{"endpoint": "s3.example.com", "bucket": "b", "access_key_id": "k"},
		{"bucket": "b", "access_key_id": "k", "secret_access_key": "s"},
	}
	for i, cfg := range partial {
		if err := d.Mount(1, cfg); err == nil {
			t.Errorf("partial config #%d should error: %v", i, cfg)
		}
	}
}

func TestMountMissingConfigErrorShape(t *testing.T) {
	d := &S3Driver{}
	err := d.Mount(1, map[string]string{})
	if err == nil {
		t.Fatal("expected error")
	}
	var de *api.DriverError
	if !errors.As(err, &de) {
		t.Fatalf("want *api.DriverError, got %T: %v", err, err)
	}
	if de.Code != 10 {
		t.Errorf("Code = %d, want 10", de.Code)
	}
}

// Every op on a zero-value (never-mounted) driver must return
// ErrNotConnected rather than panicking on a nil minio client.
func TestNotConnectedOperations(t *testing.T) {
	d := &S3Driver{}

	if _, err := d.Stat(1, "/"); err == nil {
		t.Error("Stat should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Stat: want ErrNotConnected, got %v", err)
	}

	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("ListDir: want ErrNotConnected, got %v", err)
	}

	if _, err := d.OpenFile(1, "/x"); err == nil {
		t.Error("OpenFile should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("OpenFile: want ErrNotConnected, got %v", err)
	}

	if _, err := d.CreateFile(1, "/x"); err == nil {
		t.Error("CreateFile should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("CreateFile: want ErrNotConnected, got %v", err)
	}

	if err := d.Mkdir(1, "/x"); err == nil {
		t.Error("Mkdir should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Mkdir: want ErrNotConnected, got %v", err)
	}

	if err := d.Remove(1, "/x"); err == nil {
		t.Error("Remove should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Remove: want ErrNotConnected, got %v", err)
	}

	if err := d.Rename(1, "/a", "/b"); err == nil {
		t.Error("Rename should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Rename: want ErrNotConnected, got %v", err)
	}
}

// Unmount on a fresh driver must not panic and must leave the driver in
// the not-connected state.
func TestUnmountUnmounted(t *testing.T) {
	d := &S3Driver{}
	if err := d.Unmount(1); err != nil {
		t.Fatalf("Unmount on fresh driver: %v", err)
	}
	if d.connected {
		t.Error("connected should be false after Unmount on fresh driver")
	}
	if d.client != nil {
		t.Error("client should be nil after Unmount on fresh driver")
	}
	if _, err := d.Stat(1, "/"); !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Stat after Unmount: want ErrNotConnected, got %v", err)
	}
}
