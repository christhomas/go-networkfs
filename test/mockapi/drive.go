package main

// The Google Drive routes this project's driver calls.
//
// Drive addresses files by opaque id rather than by path, and the driver
// resolves a path by querying for each component in turn. The query parser
// here understands only the two shapes the driver sends, which is the point:
// a fuller parser would accept queries the driver never makes and prove
// nothing about the ones it does.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const folderMime = "application/vnd.google-apps.folder"

func driveMeta(n *node) string {
	mime := "text/plain"
	if n.dir {
		mime = folderMime
	}
	return fmt.Sprintf(
		`{"id":%q,"name":%q,"mimeType":%q,"size":"%d","modifiedTime":%q}`,
		n.id, n.name, mime, len(n.body), n.mtime.UTC().Format("2006-01-02T15:04:05Z"))
}

// parseQuery pulls the parent id and the name out of the two query shapes the
// driver sends: "'<parent>' in parents and trashed = false", optionally with
// "and name = '<name>'".
func parseQuery(q string) (parent, name string) {
	if i := strings.Index(q, "' in parents"); i > 0 {
		if j := strings.LastIndex(q[:i], "'"); j >= 0 {
			parent = q[j+1 : i]
		}
	}
	const marker = "name = '"
	if i := strings.Index(q, marker); i >= 0 {
		rest := q[i+len(marker):]
		if j := strings.Index(rest, "'"); j >= 0 {
			name = rest[:j]
		}
	}
	return parent, name
}

// pathForID finds the store path of a Drive id. "root" is the root.
func pathForID(s *store, id string) (string, bool) {
	if id == "root" || id == "" {
		return "/", true
	}
	p, _, ok := s.byID(id)
	return p, ok
}

func registerDrive(mux *http.ServeMux, s *store) {
	// Listing and search, plus creation by metadata.
	mux.HandleFunc("/gdrive/drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var meta struct {
				Name     string   `json:"name"`
				MimeType string   `json:"mimeType"`
				Parents  []string `json:"parents"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &meta)

			parent := "/"
			if len(meta.Parents) > 0 {
				if p, ok := pathForID(s, meta.Parents[0]); ok {
					parent = p
				}
			}
			n := s.put(join(parent, meta.Name), meta.MimeType == folderMime, nil)
			writeJSON(w, http.StatusOK, fmt.Sprintf(`{"id":%q}`, n.id))
			return
		}

		parentID, name := parseQuery(r.URL.Query().Get("q"))
		parent, ok := pathForID(s, parentID)
		if !ok {
			writeJSON(w, http.StatusOK, `{"files":[]}`)
			return
		}

		var parts []string
		for _, c := range s.children(parent) {
			if name != "" && c.Node.name != name {
				continue
			}
			parts = append(parts, driveMeta(c.Node))
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"files":[%s]}`, strings.Join(parts, ",")))
	})

	// Metadata, content, update and delete, all addressed by id.
	mux.HandleFunc("/gdrive/drive/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/gdrive/drive/v3/files/")
		id = strings.TrimSuffix(id, "/export")

		path, ok := pathForID(s, id)
		if !ok {
			writeJSON(w, http.StatusNotFound, `{"error":{"message":"File not found"}}`)
			return
		}
		n, _ := s.get(path)

		switch {
		case r.Method == http.MethodDelete:
			s.remove(path)
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPatch:
			var meta struct {
				Name string `json:"name"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &meta)

			dest := path
			if meta.Name != "" {
				dest = join(parentOf(path), meta.Name)
			}
			if add := r.URL.Query().Get("addParents"); add != "" {
				if newParent, ok := pathForID(s, add); ok {
					dest = join(newParent, base(dest))
				}
			}
			s.move(path, dest)
			writeJSON(w, http.StatusOK, fmt.Sprintf(`{"id":%q}`, n.id))

		case r.URL.Query().Get("alt") == "media" || strings.HasSuffix(r.URL.Path, "/export"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(n.body)

		default:
			writeJSON(w, http.StatusOK, driveMeta(n))
		}
	})

	// Multipart upload, for both new files and updates.
	upload := func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/gdrive/upload/drive/v3/files")
		id = strings.Trim(id, "/")

		body, _ := io.ReadAll(r.Body)
		meta, content := splitMultipart(body)

		var m struct {
			Name    string   `json:"name"`
			Parents []string `json:"parents"`
		}
		_ = json.Unmarshal(meta, &m)

		path := ""
		if id != "" {
			if p, ok := pathForID(s, id); ok {
				path = p
			}
		}
		if path == "" {
			parent := "/"
			if len(m.Parents) > 0 {
				if p, ok := pathForID(s, m.Parents[0]); ok {
					parent = p
				}
			}
			path = join(parent, m.Name)
		}

		n := s.put(path, false, content)
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"id":%q}`, n.id))
	}
	mux.HandleFunc("/gdrive/upload/drive/v3/files", upload)
	mux.HandleFunc("/gdrive/upload/drive/v3/files/", upload)
}

// splitMultipart pulls the JSON part and the bytes out of the multipart/related
// body the driver builds by hand. It splits on the blank lines rather than
// parsing MIME, because the driver writes the body itself and this only has to
// read back what that writes.
func splitMultipart(body []byte) (meta, content []byte) {
	text := string(body)
	sections := strings.Split(text, "\r\n\r\n")
	if len(sections) < 3 {
		return nil, nil
	}
	// section 1 is the JSON followed by the next boundary line
	metaPart := sections[1]
	if i := strings.LastIndex(metaPart, "\r\n--"); i >= 0 {
		metaPart = metaPart[:i]
	}
	contentPart := sections[2]
	if i := strings.LastIndex(contentPart, "\r\n--"); i >= 0 {
		contentPart = contentPart[:i]
	}
	return []byte(metaPart), []byte(contentPart)
}

func join(dir, name string) string {
	if dir == "/" || dir == "" {
		return "/" + name
	}
	return strings.TrimRight(dir, "/") + "/" + name
}

func parentOf(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}
