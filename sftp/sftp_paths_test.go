package sftp

import "testing"

func TestNameFromPath(t *testing.T) {
	d := &SFTPDriver{}
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
