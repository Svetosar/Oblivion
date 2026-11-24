// SPDX-License-Identifier: AGPL-3.0-or-later

// Package admin serves the embedded admin single-page app under /admin/.
// The app is built from ./admin (Vite) into ./dist by `make admin`.
package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the admin app mounted at /admin/. Unknown paths fall back to
// index.html (client-side routing is hash based, but reloads must not 404).
// Files under assets/ have content-hashed names and are cached for a year.
func Handler() http.Handler {
	sub, _ := fs.Sub(distFS, "dist")
	return handler(sub)
}

func handler(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(strings.TrimPrefix(r.URL.Path, "/admin")), "/")
		if _, err := fs.Stat(root, "index.html"); err != nil {
			http.Error(w, "admin interface is not built (run `make admin`)", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasPrefix(name, "assets/") {
			if _, err := fs.Stat(root, name); err == nil {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + name
				files.ServeHTTP(w, r2)
				return
			}
			http.NotFound(w, r)
			return
		}
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(root, name); err == nil && !st.IsDir() {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + name
				files.ServeHTTP(w, r2)
				return
			}
		}
		// SPA entry point; served directly so FileServer does not redirect /index.html to ./.
		b, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
}
