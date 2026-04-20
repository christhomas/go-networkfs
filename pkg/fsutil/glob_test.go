package fsutil

import (
	"sort"
	"testing"
)

func TestGlobNonRecursiveAtRoot(t *testing.T) {
	d := newTree()
	got, err := Glob(d, 1, "/", "*.txt")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	// Only /a.txt matches — /sub/b.txt is in a subdir.
	if !equalSorted(got, []string{"/a.txt"}) {
		t.Fatalf("got %v", got)
	}
}

func TestGlobNonRecursiveInSubdir(t *testing.T) {
	d := newTree()
	got, err := Glob(d, 1, "/sub", "*.txt")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	// /sub/b.txt only — /sub/deep/c.txt is another level down.
	if !equalSorted(got, []string{"/sub/b.txt"}) {
		t.Fatalf("got %v", got)
	}
}

func TestGlobRecursiveDoubleStar(t *testing.T) {
	d := newTree()
	got, err := Glob(d, 1, "/", "**/*.txt")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	want := []string{"/a.txt", "/sub/b.txt", "/sub/deep/c.txt"}
	if !equalSorted(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGlobMatchesDirectories(t *testing.T) {
	d := newTree()
	// Non-recursive glob at root should also match directory entries
	// (like a shell would).
	got, err := Glob(d, 1, "/", "sub")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if !equalSorted(got, []string{"/sub"}) {
		t.Fatalf("got %v", got)
	}
}

func TestGlobBadPatternReturnsError(t *testing.T) {
	d := newTree()
	// Unclosed character class is an invalid pattern; path.Match
	// returns an error which Glob surfaces.
	if _, err := Glob(d, 1, "/", "[abc"); err == nil {
		t.Fatal("expected malformed pattern to error")
	}
}

func TestGlobNoMatchesYieldsEmpty(t *testing.T) {
	d := newTree()
	got, err := Glob(d, 1, "/", "*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no matches, got %v", got)
	}
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa, bb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
