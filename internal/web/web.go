// Package web embeds the zero-dependency Flatline SPA into the daemon binary.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

//go:embed static/*
var embedded embed.FS

func Handler() http.Handler {
	files, err := fs.Sub(embedded, "static")
	if err != nil {
		panic("web: embedded static filesystem is invalid: " + err.Error())
	}
	server := http.FileServer(http.FS(files))
	var mu sync.Mutex
	tags := map[string]string{}
	etag := func(path string) string {
		mu.Lock()
		defer mu.Unlock()
		if tag, ok := tags[path]; ok {
			return tag
		}
		data, err := fs.ReadFile(files, path)
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(data)
		tag := `"` + hex.EncodeToString(sum[:8]) + `"`
		tags[path] = tag
		return tag
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(files, path); err != nil {
			// Hash routes and unknown browser paths are handled by the SPA.
			r.URL.Path = "/"
			path = "index.html"
		}
		// A rebuilt binary ships new static files under the same URLs, so the
		// browser must revalidate every time; the content hash makes that cheap.
		w.Header().Set("Cache-Control", "no-cache")
		if tag := etag(path); tag != "" {
			w.Header().Set("ETag", tag)
			if r.Header.Get("If-None-Match") == tag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		server.ServeHTTP(w, r)
	})
}
