package handlers

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/reynerpantou/libra/internal/assets"
)

// SPAHandler serves the embedded React build. Real files are served directly;
// any other path falls back to index.html so client-side routes work on reload.
func (s *Server) SPAHandler() http.Handler {
	sub, err := fs.Sub(assets.FS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := sub.Open(p); err == nil {
				info, _ := f.Stat()
				f.Close()
				if info != nil && !info.IsDir() {
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
