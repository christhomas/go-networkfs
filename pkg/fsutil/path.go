package fsutil

import "strings"

// NameFromPath returns the last non-empty segment of a slash-separated path:
// the entry's own name, without the directory that holds it.
//
//	/a/b/c.txt -> "c.txt"
//	/a/b/      -> "b"
//	/          -> ""
//
// The root has no name, so it yields an empty string rather than "/". A driver
// filling api.FileInfo.Name for the root of a mount should leave it empty; the
// path already says where it is, and the name is meant to be what you would
// display beside it.
//
// This lived in six drivers, in two versions that disagreed about exactly that
// case, so FTP and WebDAV reported the root as "/" while S3, Google Drive and
// Dropbox reported it as "". They implement one interface and a caller should
// not have to know which backend answered.
func NameFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}
