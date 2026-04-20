package fsutil

import (
	"path"
	"strings"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// Glob returns all paths under root whose basename matches pattern.
// pattern uses the same shell-glob syntax as path.Match: '*' matches
// any sequence except separators, '?' matches a single character, and
// '[...]' is a character class.
//
// To match across directory levels use "**/": "**/*.txt" visits every
// .txt file in the tree. This is a stable extension on top of
// path.Match — Go's stdlib globs don't match across separators.
//
// Returned paths are in the same lexical order Walk produces.
func Glob(d api.Driver, mountID int, root, pattern string) ([]string, error) {
	recursive := strings.HasPrefix(pattern, "**/")
	matchPat := pattern
	if recursive {
		matchPat = strings.TrimPrefix(pattern, "**/")
	}
	var out []string
	err := Walk(d, mountID, root, func(p string, info api.FileInfo, err error) error {
		if err != nil {
			return nil // keep walking; skip this subtree
		}
		// Non-recursive: only match entries whose parent equals root.
		if !recursive && parentPath(p) != normaliseRoot(root) {
			return nil
		}
		ok, perr := path.Match(matchPat, info.Name)
		if perr != nil {
			return perr
		}
		if ok && info.Name != "" {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// parentPath returns the parent of p, or "/" for top-level paths.
func parentPath(p string) string {
	p = trimRightSlash(p)
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func normaliseRoot(r string) string {
	if r == "" {
		return "/"
	}
	return trimRightSlash(r)
}
