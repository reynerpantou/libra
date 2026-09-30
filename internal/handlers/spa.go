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
		// The web app isn't built into this binary (e.g. `go run` without
		// `make web`). The API still works; say how to get the UI.
		index = []byte(notBuilt)
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

const notBuilt = `<!doctype html>
<html lang="en">
  <head><meta charset="UTF-8" /><title>Libra</title></head>
  <body style="font-family: system-ui; padding: 2rem">
    <p>The Libra web app isn't built into this binary. Run <code>make build</code> (or <code>make web</code>, then rebuild) and restart.</p>
  </body>
</html>`
