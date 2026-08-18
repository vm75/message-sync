package api

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

//go:embed web/*
var webFS embed.FS

// StaticHandler returns an http.Handler that serves embedded static assets
// and falls back to index.html for client-side SPA routing.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("failed to create sub fs for web: " + err.Error())
	}

	indexBytes, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("failed to read embedded index.html: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		cleanPath := path.Clean("/" + r.URL.Path)
		trimmedPath := strings.TrimPrefix(cleanPath, "/")

		if trimmedPath != "" && trimmedPath != "index.html" {
			f, err := sub.Open(trimmedPath)
			if err == nil {
				defer f.Close()
				stat, err := f.Stat()
				if err == nil && !stat.IsDir() {
					ext := filepath.Ext(trimmedPath)
					if ct := mime.TypeByExtension(ext); ct != "" {
						w.Header().Set("Content-Type", ct)
					}
					if strings.HasPrefix(trimmedPath, "css/") || strings.HasPrefix(trimmedPath, "js/") {
						w.Header().Set("Cache-Control", "public, max-age=3600")
					}
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}

		// Fallback to index.html for root and SPA client routes
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(indexBytes))
	})
}
