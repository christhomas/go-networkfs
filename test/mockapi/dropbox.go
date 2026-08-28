package main

// The Dropbox routes this project's driver calls.
//
// The driver reaches them through the SDK's URLGenerator hook, which carries
// the host type in the path, so an API call and a content transfer arrive on
// different prefixes exactly as they would against the real service.

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// dbxPath converts a Dropbox path to a store path. Dropbox spells the root as
// the empty string.
func dbxPath(p string) string {
	if p == "" {
		return "/"
	}
	return normalise(p)
}

func dbxMeta(path string, n *node) string {
	tag := "file"
	if n.dir {
		tag = "folder"
	}
	name := n.name
	if name == "" {
		name = "root"
	}
	display := path
	if display == "/" {
		display = ""
	}
	if n.dir {
		return fmt.Sprintf(`{".tag":%q,"name":%q,"path_lower":%q,"path_display":%q,"id":"id:%s"}`,
			tag, name, strings.ToLower(display), display, n.id)
	}
	return fmt.Sprintf(
		`{".tag":%q,"name":%q,"path_lower":%q,"path_display":%q,"id":"id:%s",`+
			`"size":%d,"rev":"a1","client_modified":%q,"server_modified":%q}`,
		tag, name, strings.ToLower(display), display, n.id,
		len(n.body), n.mtime.UTC().Format("2006-01-02T15:04:05Z"),
		n.mtime.UTC().Format("2006-01-02T15:04:05Z"))
}

func dbxNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict,
		`{"error_summary":"path/not_found/","error":{".tag":"path","path":{".tag":"not_found"}}}`)
}

func registerDropbox(mux *http.ServeMux, s *store) {
	handle := func(suffix string, fn func(http.ResponseWriter, *http.Request)) {
		// The SDK's host type sits in the path, so both prefixes are served.
		mux.HandleFunc("/dropbox/api/2/files/"+suffix, fn)
		mux.HandleFunc("/dropbox/content/2/files/"+suffix, fn)
	}

	handle("get_metadata", func(w http.ResponseWriter, r *http.Request) {
		p := dbxPath(argString(jsonArg(r), "path"))
		n, ok := s.get(p)
		if !ok {
			dbxNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, dbxMeta(p, n))
	})

	handle("list_folder", func(w http.ResponseWriter, r *http.Request) {
		p := dbxPath(argString(jsonArg(r), "path"))
		if _, ok := s.get(p); !ok {
			dbxNotFound(w)
			return
		}
		var parts []string
		for _, c := range s.children(p) {
			parts = append(parts, dbxMeta(c.Path, c.Node))
		}
		writeJSON(w, http.StatusOK,
			fmt.Sprintf(`{"entries":[%s],"cursor":"c1","has_more":false}`, strings.Join(parts, ",")))
	})

	handle("create_folder_v2", func(w http.ResponseWriter, r *http.Request) {
		p := dbxPath(argString(jsonArg(r), "path"))
		n := s.put(p, true, nil)
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"metadata":%s}`, dbxMeta(p, n)))
	})

	handle("delete_v2", func(w http.ResponseWriter, r *http.Request) {
		p := dbxPath(argString(jsonArg(r), "path"))
		n, ok := s.get(p)
		if !ok {
			dbxNotFound(w)
			return
		}
		meta := dbxMeta(p, n)
		s.remove(p)
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"metadata":%s}`, meta))
	})

	handle("move_v2", func(w http.ResponseWriter, r *http.Request) {
		a := jsonArg(r)
		from := dbxPath(argString(a, "from_path"))
		to := dbxPath(argString(a, "to_path"))
		if !s.move(from, to) {
			dbxNotFound(w)
			return
		}
		n, _ := s.get(to)
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"metadata":%s}`, dbxMeta(to, n)))
	})

	handle("download", func(w http.ResponseWriter, r *http.Request) {
		p := dbxPath(argString(jsonArg(r), "path"))
		n, ok := s.get(p)
		if !ok || n.dir {
			dbxNotFound(w)
			return
		}
		w.Header().Set("Dropbox-API-Result", dbxMeta(p, n))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(n.body)
	})

	handle("upload", func(w http.ResponseWriter, r *http.Request) {
		p := dbxPath(argString(jsonArg(r), "path"))
		body, _ := io.ReadAll(r.Body)
		n := s.put(p, false, body)
		writeJSON(w, http.StatusOK, dbxMeta(p, n))
	})
}
