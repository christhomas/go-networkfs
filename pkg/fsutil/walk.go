// Package fsutil provides free-function helpers that compose on top of
// api.Driver. Everything here is driver-agnostic and lives outside the
// core interface so pkg/api stays minimal.
package fsutil

import (
	"errors"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// SkipDir, when returned by WalkFunc on a directory entry, tells Walk
// not to descend into that directory. Modelled on filepath.SkipDir so
// the ergonomics match what callers already know.
var SkipDir = errors.New("skip this directory")

// WalkFunc is invoked for every entry Walk visits. It receives the
// entry's path, its FileInfo, and any error encountered while listing
// the parent directory (in which case info.IsDir is unreliable).
//
// Return SkipDir on a directory to skip its descendants. Any other
// non-nil return aborts the walk and surfaces that error from Walk.
type WalkFunc func(path string, info api.FileInfo, err error) error

// Walk traverses the tree rooted at root in lexical order per directory,
// invoking fn for each entry including root itself. Directories are
// visited before their contents.
//
// Errors from ListDir are reported to fn with a zeroed FileInfo. If fn
// returns SkipDir the subtree is skipped; any other non-nil return
// aborts the whole walk and propagates up.
func Walk(d api.Driver, mountID int, root string, fn WalkFunc) error {
	info, err := d.Stat(mountID, root)
	if err != nil {
		// Surface the stat failure to the caller via fn, same as
		// filepath.WalkDir does. fn decides whether to continue.
		return fn(root, api.FileInfo{Path: root}, err)
	}
	return walk(d, mountID, info, fn)
}

func walk(d api.Driver, mountID int, info api.FileInfo, fn WalkFunc) error {
	// Emit the current entry first.
	if err := fn(info.Path, info, nil); err != nil {
		if errors.Is(err, SkipDir) && info.IsDir {
			return nil
		}
		return err
	}
	if !info.IsDir {
		return nil
	}

	entries, err := d.ListDir(mountID, info.Path)
	if err != nil {
		// Give fn a chance to swallow the error and keep walking peers.
		if cbErr := fn(info.Path, info, err); cbErr != nil {
			if errors.Is(cbErr, SkipDir) {
				return nil
			}
			return cbErr
		}
		return nil
	}
	for _, e := range entries {
		// Ensure the child's Path is set correctly. Drivers are supposed
		// to populate this but the walk shouldn't trust that blindly.
		if e.Path == "" {
			e.Path = joinPath(info.Path, e.Name)
		}
		if err := walk(d, mountID, e, fn); err != nil {
			return err
		}
	}
	return nil
}

func joinPath(dir, name string) string {
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return trimRightSlash(dir) + "/" + name
}

func trimRightSlash(s string) string {
	for len(s) > 1 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
