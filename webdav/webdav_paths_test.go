package webdav

// Tests for path construction.
//
// fullPath joins a configured prefix onto a caller's path, and the prefix may
// or may not have leading and trailing slashes depending on how it was
// configured. Getting that wrong yields a doubled or missing separator, which
// a server answers with a 404 that looks like a missing file rather than a
// malformed request.

import "testing"

func TestFullPathWithoutPrefix(t *testing.T) {
	d := &WebDAVDriver{}
	for _, p := range []string{"/", "/a", "/a/b.txt"} {
		if got := d.fullPath(p); got != p {
			t.Errorf("fullPath(%q) = %q, want it unchanged when no prefix is set", p, got)
		}
	}
}

// The prefix is accepted in any of the four slash arrangements, and all four
// have to produce the same result.
func TestFullPathNormalisesPrefixSlashes(t *testing.T) {
	for _, prefix := range []string{"dav", "/dav", "dav/", "/dav/"} {
		d := &WebDAVDriver{pathPrefix: prefix}
		for _, tt := range []struct{ in, want string }{
			{"/a.txt", "/dav/a.txt"},
			{"a.txt", "/dav/a.txt"},
			{"/", "/dav/"},
		} {
			got := d.fullPath(tt.in)
			if got != tt.want {
				t.Errorf("prefix %q: fullPath(%q) = %q, want %q", prefix, tt.in, got, tt.want)
			}
		}
	}
}

func TestNameFromPath(t *testing.T) {
	d := &WebDAVDriver{}
	for _, tt := range []struct{ in, want string }{
		{"/a/b/c.txt", "c.txt"},
		{"/a/b/", "b"},
		{"/single", "single"},
		// Every segment of "/" is empty, so the documented fallback returns
		// the path itself rather than an empty name.
		{"/", "/"},
		{"", ""},
	} {
		if got := d.nameFromPath(tt.in); got != tt.want {
			t.Errorf("nameFromPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
