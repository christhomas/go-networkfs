package main

// The Microsoft Graph routes this project's OneDrive driver calls.
//
// Graph addresses items by path rather than by id, in the form
// /me/drive/root:/some/path:/action, so the work here is mostly pulling the
// path back out of that shape.

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

func graphMeta(n *node) string {
	if n.dir {
		return fmt.Sprintf(
			`{"name":%q,"size":%d,"lastModifiedDateTime":%q,"folder":{"childCount":0}}`,
			n.name, len(n.body), n.mtime.UTC().Format("2006-01-02T15:04:05Z"))
	}
	return fmt.Sprintf(
		`{"name":%q,"size":%d,"lastModifiedDateTime":%q,"file":{"mimeType":"application/octet-stream"}}`,
		n.name, len(n.body), n.mtime.UTC().Format("2006-01-02T15:04:05Z"))
}

// graphTarget splits a Graph item URL into the item path and the trailing
// action, if any.
//
//	/me/drive/root                      -> "/",        ""
//	/me/drive/root:/a/b.txt             -> "/a/b.txt", ""
//	/me/drive/root:/a:/children         -> "/a",       "children"
//	/me/drive/root/children             -> "/",        "children"
func graphTarget(p string) (path, action string) {
	p = strings.TrimPrefix(p, "/onedrive/v1.0/me/drive/root")

	switch {
	case p == "" || p == "/":
		return "/", ""
	case strings.HasPrefix(p, ":"):
		rest := p[1:]
		if i := strings.Index(rest, ":/"); i >= 0 {
			return normalise(rest[:i]), rest[i+2:]
		}
		return normalise(rest), ""
	default:
		return "/", strings.TrimPrefix(p, "/")
	}
}

func registerGraph(mux *http.ServeMux, s *store) {
	mux.HandleFunc("/onedrive/v1.0/me/drive/root", func(w http.ResponseWriter, r *http.Request) {
		graphHandle(w, r, s)
	})
	mux.HandleFunc("/onedrive/v1.0/me/drive/root/", func(w http.ResponseWriter, r *http.Request) {
		graphHandle(w, r, s)
	})
	// Graph's colon syntax means the path does not always start with a slash
	// after "root", so the bare prefix has to be registered too.
	mux.HandleFunc("/onedrive/v1.0/", func(w http.ResponseWriter, r *http.Request) {
		graphHandle(w, r, s)
	})
}

func graphHandle(w http.ResponseWriter, r *http.Request, s *store) {
	path, action := graphTarget(r.URL.Path)

	switch {
	case action == "children" && r.Method == http.MethodGet:
		if _, ok := s.get(path); !ok {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":"itemNotFound"}}`)
			return
		}
		var parts []string
		for _, c := range s.children(path) {
			parts = append(parts, graphMeta(c.Node))
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"value":[%s]}`, strings.Join(parts, ",")))

	case action == "children" && r.Method == http.MethodPost:
		var meta struct {
			Name string `json:"name"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = decodeJSON(body, &meta)
		n := s.put(join(path, meta.Name), true, nil)
		writeJSON(w, http.StatusCreated, graphMeta(n))

	case action == "content" && r.Method == http.MethodGet:
		n, ok := s.get(path)
		if !ok || n.dir {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":"itemNotFound"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(n.body)

	case action == "content" && r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		n := s.put(path, false, body)
		writeJSON(w, http.StatusCreated, graphMeta(n))

	case action == "createUploadSession":
		// The upload URL is served below; it carries the target path so the
		// chunk handler knows where the bytes belong.
		writeJSON(w, http.StatusOK, fmt.Sprintf(
			`{"uploadUrl":"http://%s/onedrive/upload?path=%s"}`, r.Host, path))

	case r.Method == http.MethodDelete:
		if !s.remove(path) {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":"itemNotFound"}}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPatch:
		var meta struct {
			Name            string `json:"name"`
			ParentReference *struct {
				Path string `json:"path"`
			} `json:"parentReference"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = decodeJSON(body, &meta)

		dest := path
		if meta.ParentReference != nil {
			parent := strings.TrimPrefix(meta.ParentReference.Path, "/drive/root:")
			dest = join(normalise(parent), meta.Name)
		} else if meta.Name != "" {
			dest = join(parentOf(path), meta.Name)
		}
		if !s.move(path, dest) {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":"itemNotFound"}}`)
			return
		}
		n, _ := s.get(dest)
		writeJSON(w, http.StatusOK, graphMeta(n))

	default:
		n, ok := s.get(path)
		if !ok {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":"itemNotFound"}}`)
			return
		}
		writeJSON(w, http.StatusOK, graphMeta(n))
	}
}
