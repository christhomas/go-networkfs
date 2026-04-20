package fsutil

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// memDriver is a tiny in-memory api.Driver implementation used to
// exercise Walk without needing a real backend. Supports only Stat and
// ListDir — that's all Walk touches.
type memDriver struct {
	// tree maps a parent path to its (direct) children. Leaves are
	// represented by having no entry in tree.
	tree    map[string][]api.FileInfo
	statErr map[string]error // path -> error to return from Stat
	listErr map[string]error // path -> error to return from ListDir
}

func (m *memDriver) Name() string                       { return "mem" }
func (m *memDriver) Mount(int, map[string]string) error { return nil }
func (m *memDriver) Unmount(int) error                  { return nil }
func (m *memDriver) OpenFile(int, string) (io.ReadCloser, error) {
	return nil, errors.New("unsupported")
}
func (m *memDriver) CreateFile(int, string) (io.WriteCloser, error) {
	return nil, errors.New("unsupported")
}
func (m *memDriver) Mkdir(int, string) error          { return nil }
func (m *memDriver) Remove(int, string) error         { return nil }
func (m *memDriver) Rename(int, string, string) error { return nil }

func (m *memDriver) Stat(_ int, path string) (api.FileInfo, error) {
	if err, ok := m.statErr[path]; ok {
		return api.FileInfo{}, err
	}
	// If the path is a known parent it's a directory.
	if _, ok := m.tree[path]; ok {
		return api.FileInfo{Path: path, Name: baseName(path), IsDir: true}, nil
	}
	// Otherwise look for it in its parent's child list.
	parent := parentOf(path)
	for _, e := range m.tree[parent] {
		if e.Path == path {
			return e, nil
		}
	}
	return api.FileInfo{}, errors.New("not found: " + path)
}

func (m *memDriver) ListDir(_ int, path string) ([]api.FileInfo, error) {
	if err, ok := m.listErr[path]; ok {
		return nil, err
	}
	return m.tree[path], nil
}

func baseName(p string) string {
	if p == "/" {
		return ""
	}
	p = strings.TrimRight(p, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func parentOf(p string) string {
	p = strings.TrimRight(p, "/")
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// newTree builds a memDriver rooted at / with this layout:
//
//	/
//	├── a.txt
//	├── sub/
//	│   ├── b.txt
//	│   └── deep/
//	│       └── c.txt
//	└── empty/
func newTree() *memDriver {
	return &memDriver{
		tree: map[string][]api.FileInfo{
			"/": {
				{Name: "a.txt", Path: "/a.txt"},
				{Name: "sub", Path: "/sub", IsDir: true},
				{Name: "empty", Path: "/empty", IsDir: true},
			},
			"/sub": {
				{Name: "b.txt", Path: "/sub/b.txt"},
				{Name: "deep", Path: "/sub/deep", IsDir: true},
			},
			"/sub/deep": {
				{Name: "c.txt", Path: "/sub/deep/c.txt"},
			},
			"/empty": nil,
		},
	}
}

func TestWalkVisitsEveryEntryOnce(t *testing.T) {
	d := newTree()
	var seen []string
	err := Walk(d, 1, "/", func(path string, _ api.FileInfo, err error) error {
		if err != nil {
			t.Errorf("unexpected err on %s: %v", path, err)
		}
		seen = append(seen, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := []string{
		"/", "/a.txt", "/sub", "/sub/b.txt", "/sub/deep", "/sub/deep/c.txt", "/empty",
	}
	if len(seen) != len(want) {
		t.Fatalf("visited %d entries, want %d (%v)", len(seen), len(want), seen)
	}
	for i, w := range want {
		if seen[i] != w {
			t.Errorf("visited[%d] = %q, want %q", i, seen[i], w)
		}
	}
}

func TestWalkSkipDirSkipsSubtree(t *testing.T) {
	d := newTree()
	var seen []string
	err := Walk(d, 1, "/", func(path string, info api.FileInfo, _ error) error {
		seen = append(seen, path)
		if info.IsDir && path == "/sub" {
			return SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	for _, p := range seen {
		if strings.HasPrefix(p, "/sub/") {
			t.Errorf("unexpected descendant of /sub: %s", p)
		}
	}
	// /sub itself should still have been visited.
	found := false
	for _, p := range seen {
		if p == "/sub" {
			found = true
		}
	}
	if !found {
		t.Error("/sub should have been visited before skip")
	}
}

func TestWalkPropagatesCallbackError(t *testing.T) {
	d := newTree()
	want := errors.New("stop now")
	err := Walk(d, 1, "/", func(path string, _ api.FileInfo, _ error) error {
		if path == "/sub/b.txt" {
			return want
		}
		return nil
	})
	if err != want {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestWalkStatErrorGoesToCallback(t *testing.T) {
	d := newTree()
	d.statErr = map[string]error{"/": errors.New("boom")}
	var gotErr error
	err := Walk(d, 1, "/", func(_ string, _ api.FileInfo, e error) error {
		gotErr = e
		return e
	})
	if gotErr == nil {
		t.Fatal("callback never received the stat error")
	}
	if err == nil {
		t.Fatal("Walk should surface the callback error")
	}
}

func TestWalkListErrorIsRecoverable(t *testing.T) {
	// If fn swallows the ListDir error (returns nil) the walk should
	// continue with peer entries. Here /sub fails to list but /empty
	// after it should still be visited when Walk is called from above.
	d := newTree()
	d.listErr = map[string]error{"/sub": errors.New("bad list")}
	var listErrSeen bool
	var visited []string
	err := Walk(d, 1, "/", func(path string, _ api.FileInfo, err error) error {
		if err != nil {
			listErrSeen = true
			return nil // recover
		}
		visited = append(visited, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !listErrSeen {
		t.Error("list error should have been delivered to callback")
	}
	foundEmpty := false
	for _, p := range visited {
		if p == "/empty" {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Error("/empty should still be visited after recoverable list error")
	}
}

func TestWalkNonDirRoot(t *testing.T) {
	d := newTree()
	var seen []string
	err := Walk(d, 1, "/a.txt", func(path string, _ api.FileInfo, _ error) error {
		seen = append(seen, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 1 || seen[0] != "/a.txt" {
		t.Fatalf("want single visit of /a.txt, got %v", seen)
	}
}

func TestJoinPath(t *testing.T) {
	cases := []struct{ dir, name, want string }{
		{"", "a", "/a"},
		{"/", "a", "/a"},
		{"/x", "a", "/x/a"},
		{"/x/", "a", "/x/a"},
		{"/x//", "a", "/x/a"},
	}
	for _, c := range cases {
		if got := joinPath(c.dir, c.name); got != c.want {
			t.Errorf("joinPath(%q,%q) = %q, want %q", c.dir, c.name, got, c.want)
		}
	}
}

func TestSkipDirOnNonDirIsStopMarker(t *testing.T) {
	// Returning SkipDir on a non-directory entry must NOT swallow — it
	// has no skippable subtree, so we treat it as an abort signal.
	d := newTree()
	err := Walk(d, 1, "/", func(path string, info api.FileInfo, _ error) error {
		if path == "/a.txt" {
			return SkipDir
		}
		return nil
	})
	if err == nil {
		t.Fatal("SkipDir on a non-directory should surface as an error")
	}
	if !errors.Is(err, SkipDir) {
		t.Errorf("err = %v, want SkipDir", err)
	}
}
