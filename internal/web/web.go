// Package web embeds the zero-dependency Flatline SPA into the daemon binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var embedded embed.FS

func Handler() http.Handler {
	files, err := fs.Sub(embedded, "static")
	if err != nil {
		panic("web: embedded static filesystem is invalid: " + err.Error())
	}
	server := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(files, path); err != nil {
			// Hash routes and unknown browser paths are handled by the SPA.
			r.URL.Path = "/"
		}
		server.ServeHTTP(w, r)
	})
}
