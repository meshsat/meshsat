package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

//go:embed all:web/dist
var embeddedWeb embed.FS

// webHandler returns an http.Handler that serves the web UI.
// If webDir is non-empty, it serves from disk (dev mode).
// Otherwise it serves from the embedded filesystem (production).
func webHandler(webDir string) http.Handler {
	if webDir != "" {
		// Dev mode: serve from local directory
		return spaHandler(http.Dir(webDir))
	}

	// Production: serve from embedded FS
	sub, err := fs.Sub(embeddedWeb, "web/dist")
	if err != nil {
		// If embed dir doesn't exist (built without web), serve 404
		return http.NotFoundHandler()
	}
	return spaHandler(http.FS(sub))
}

// spaHandler serves static files with SPA routing fallback.
// Any path that doesn't match a real file gets index.html.
func spaHandler(root http.FileSystem) http.Handler {
	fileServer := http.FileServer(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Try opening the file
		f, err := root.Open(path)
		if err != nil {
			// File not found — serve index.html for SPA routing
			if os.IsNotExist(err) || strings.Contains(err.Error(), "file does not exist") {
				r.URL.Path = "/"
				fileServer.ServeHTTP(w, r)
				return
			}
			http.NotFoundHandler().ServeHTTP(w, r)
			return
		}
		f.Close()

		fileServer.ServeHTTP(w, r)
	})
}
