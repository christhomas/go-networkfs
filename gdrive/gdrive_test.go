// gdrive/gdrive_test.go - Unit tests for pure helpers and driver plumbing.

package gdrive

import (
	"testing"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// ---------------------------------------------------------------------------
// exportMimeFor
// ---------------------------------------------------------------------------

func TestExportMimeFor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"spreadsheet", "application/vnd.google-apps.spreadsheet", "text/csv"},
		{"drawing", "application/vnd.google-apps.drawing", "image/png"},
		{"document_defaults_to_pdf", "application/vnd.google-apps.document", "application/pdf"},
		{"presentation_defaults_to_pdf", "application/vnd.google-apps.presentation", "application/pdf"},
		{"unknown_google_type_defaults_to_pdf", "application/vnd.google-apps.form", "application/pdf"},
		{"unknown_non_google_mime_defaults_to_pdf", "application/x-unknown", "application/pdf"},
		{"empty_defaults_to_pdf", "", "application/pdf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exportMimeFor(tc.in)
			if got != tc.want {
				t.Fatalf("exportMimeFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// normPath
// ---------------------------------------------------------------------------

func TestNormPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty_string", "", "/"},
		{"root", "/", "/"},
		{"no_leading_slash_single_segment", "foo", "/foo"},
		{"no_leading_slash_multi_segment", "foo/bar", "/foo/bar"},
		{"leading_slash_already", "/foo", "/foo"},
		{"trailing_slash_trimmed", "/foo/", "/foo"},
		{"multiple_trailing_slashes_trimmed", "/foo///", "/foo"},
		{"nested_with_trailing_slash", "/a/b/c/", "/a/b/c"},
		{"unicode_name", "/dossier/éléphant", "/dossier/éléphant"},
		{"space_in_name", "/My Documents/file.txt", "/My Documents/file.txt"},
		{"no_slash_with_space", "My Docs", "/My Docs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normPath(tc.in)
			if got != tc.want {
				t.Fatalf("normPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormPathDoubleSlashOnly documents current observed behaviour of
// normPath on a "//" input. The early-return clause only matches the exact
// "/" string, so "//" falls through and TrimRight strips both slashes,
// returning "". This is arguably a bug (callers treat the empty string as
// "not a path") but it is the function's current contract, so the test
// pins it. If you change the contract, update the expected value here.
func TestNormPathDoubleSlashOnly(t *testing.T) {
	got := normPath("//")
	if got != "" {
		t.Logf("normPath(\"//\") = %q; previously observed = %q", got, "")
	}
	// Pin current behaviour so regressions surface loudly.
	if got != "" {
		t.Fatalf("normPath(\"//\") = %q, want %q (current behaviour)", got, "")
	}
}

// ---------------------------------------------------------------------------
// splitParent
// ---------------------------------------------------------------------------

func TestSplitParent(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantParent string
		wantName   string
	}{
		{"root", "/", "/", ""},
		{"empty_string_is_root", "", "/", ""},
		{"single_segment_rooted", "/foo", "/", "foo"},
		{"single_segment_unrooted", "foo", "/", "foo"},
		{"two_segments", "/a/b", "/a", "b"},
		{"three_segments", "/a/b/c", "/a/b", "c"},
		{"trailing_slash_trimmed_first", "/a/b/", "/a", "b"},
		{"unicode", "/papa/éléphant", "/papa", "éléphant"},
		{"with_space", "/a/My File.txt", "/a", "My File.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, n := splitParent(tc.in)
			if p != tc.wantParent || n != tc.wantName {
				t.Fatalf("splitParent(%q) = (%q, %q), want (%q, %q)",
					tc.in, p, n, tc.wantParent, tc.wantName)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// nameFromPath
// ---------------------------------------------------------------------------

func TestNameFromPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"root_only", "/", ""},
		{"only_slashes", "///", ""},
		{"single_rooted", "/foo", "foo"},
		{"two_segments", "/a/b", "b"},
		{"trailing_slash_ignored", "/a/b/", "b"},
		{"multiple_trailing_slashes_ignored", "/a/b///", "b"},
		{"unrooted_single", "foo", "foo"},
		{"unicode", "/a/éléphant", "éléphant"},
		{"space_name", "/a/My File.txt", "My File.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nameFromPath(tc.in)
			if got != tc.want {
				t.Fatalf("nameFromPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// strOrDefault
// ---------------------------------------------------------------------------

func TestStrOrDefault(t *testing.T) {
	type weirdStruct struct{ X int }

	cases := []struct {
		name string
		in   interface{}
		def  string
		want string
	}{
		{"non_empty_string", "hello", "def", "hello"},
		{"empty_string_falls_back", "", "def", "def"},
		{"nil_falls_back", nil, "def", "def"},
		{"int_falls_back", 42, "def", "def"},
		{"struct_falls_back", weirdStruct{X: 1}, "def", "def"},
		{"bytes_fall_back", []byte("hello"), "def", "def"},
		{"bool_falls_back", true, "def", "def"},
		{"unicode_string_kept", "éléphant", "def", "éléphant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strOrDefault(tc.in, tc.def)
			if got != tc.want {
				t.Fatalf("strOrDefault(%v, %q) = %q, want %q", tc.in, tc.def, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Driver plumbing (no network)
// ---------------------------------------------------------------------------

func TestDriverName(t *testing.T) {
	d := &GDriveDriver{}
	if got := d.Name(); got != "gdrive" {
		t.Fatalf("Name() = %q, want %q", got, "gdrive")
	}
}

func TestDriverTypeID(t *testing.T) {
	if DriverTypeID != 6 {
		t.Fatalf("DriverTypeID = %d, want 6", DriverTypeID)
	}
}

func TestDriverRegistered(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("api.GetDriver(%d) not registered", DriverTypeID)
	}
	if d == nil {
		t.Fatalf("api.GetDriver(%d) returned nil driver", DriverTypeID)
	}
	if _, ok := d.(*GDriveDriver); !ok {
		t.Fatalf("api.GetDriver(%d) returned %T, want *GDriveDriver", DriverTypeID, d)
	}
	if d.Name() != "gdrive" {
		t.Fatalf("registered driver Name() = %q, want %q", d.Name(), "gdrive")
	}
}

// TestNotConnectedGuards verifies that every Driver method that requires a
// live connection returns an error when called on a freshly constructed
// (unconnected) driver. Mount() and Unmount() are excluded as they manage
// the connection lifecycle; Mount without required config returns a
// different error path which is also exercised here.
func TestNotConnectedGuards(t *testing.T) {
	t.Run("Stat", func(t *testing.T) {
		d := &GDriveDriver{}
		if _, err := d.Stat(1, "/foo"); err == nil {
			t.Fatalf("Stat: want error, got nil")
		} else if err != api.ErrNotConnected {
			t.Fatalf("Stat: want api.ErrNotConnected, got %v", err)
		}
	})
	t.Run("ListDir", func(t *testing.T) {
		d := &GDriveDriver{}
		if _, err := d.ListDir(1, "/"); err == nil {
			t.Fatalf("ListDir: want error, got nil")
		} else if err != api.ErrNotConnected {
			t.Fatalf("ListDir: want api.ErrNotConnected, got %v", err)
		}
	})
	t.Run("OpenFile", func(t *testing.T) {
		d := &GDriveDriver{}
		if _, err := d.OpenFile(1, "/foo"); err == nil {
			t.Fatalf("OpenFile: want error, got nil")
		} else if err != api.ErrNotConnected {
			t.Fatalf("OpenFile: want api.ErrNotConnected, got %v", err)
		}
	})
	t.Run("CreateFile", func(t *testing.T) {
		d := &GDriveDriver{}
		if _, err := d.CreateFile(1, "/foo"); err == nil {
			t.Fatalf("CreateFile: want error, got nil")
		} else if err != api.ErrNotConnected {
			t.Fatalf("CreateFile: want api.ErrNotConnected, got %v", err)
		}
	})
	t.Run("Mkdir", func(t *testing.T) {
		d := &GDriveDriver{}
		if err := d.Mkdir(1, "/foo"); err == nil {
			t.Fatalf("Mkdir: want error, got nil")
		} else if err != api.ErrNotConnected {
			t.Fatalf("Mkdir: want api.ErrNotConnected, got %v", err)
		}
	})
	t.Run("Remove", func(t *testing.T) {
		d := &GDriveDriver{}
		if err := d.Remove(1, "/foo"); err == nil {
			t.Fatalf("Remove: want error, got nil")
		} else if err != api.ErrNotConnected {
			t.Fatalf("Remove: want api.ErrNotConnected, got %v", err)
		}
	})
	t.Run("Rename", func(t *testing.T) {
		d := &GDriveDriver{}
		if err := d.Rename(1, "/foo", "/bar"); err == nil {
			t.Fatalf("Rename: want error, got nil")
		} else if err != api.ErrNotConnected {
			t.Fatalf("Rename: want api.ErrNotConnected, got %v", err)
		}
	})
}

// TestMountRequiresConfig verifies Mount() rejects missing required fields
// without making any network calls.
func TestMountRequiresConfig(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]string
	}{
		{"empty_config", map[string]string{}},
		{"missing_client_secret", map[string]string{"client_id": "a", "refresh_token": "c"}},
		{"missing_refresh_token", map[string]string{"client_id": "a", "client_secret": "b"}},
		{"missing_client_id", map[string]string{"client_secret": "b", "refresh_token": "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &GDriveDriver{}
			if err := d.Mount(1, tc.config); err == nil {
				t.Fatalf("Mount(%v): want error, got nil", tc.config)
			}
		})
	}
}

// TestUnmountIdempotent documents that Unmount on a never-mounted driver
// returns nil (it simply clears nil caches). This is not asserted as a
// contract the driver must uphold; it captures current behaviour.
func TestUnmountOnUnmounted(t *testing.T) {
	d := &GDriveDriver{}
	if err := d.Unmount(1); err != nil {
		t.Fatalf("Unmount on unmounted driver: want nil, got %v", err)
	}
}
