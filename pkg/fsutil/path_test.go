package fsutil

import "testing"

func TestNameFromPath(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"/a/b/c.txt", "c.txt"},
		{"/a/b/", "b"},
		{"/single", "single"},
		{"single", "single"},
		{"a/b/c", "c"},

		// The root has no name, whichever way it is spelled.
		{"/", ""},
		{"", ""},
		{"//", ""},
		{"///", ""},
	} {
		if got := NameFromPath(tt.in); got != tt.want {
			t.Errorf("NameFromPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
