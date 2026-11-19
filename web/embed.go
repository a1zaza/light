package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist fallback public
var assets embed.FS

func Handler() http.Handler {
	publicRoot, _ := fs.Sub(assets, "public")
	publicFiles := http.FileServer(http.FS(publicRoot))
	root, _ := fs.Sub(assets, "dist")
	if _, err := fs.Stat(root, "index.html"); err != nil {
		root, _ = fs.Sub(assets, "fallback")
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(publicRoot, name); err == nil && !info.IsDir() {
			publicFiles.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(root, name); err != nil {
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
