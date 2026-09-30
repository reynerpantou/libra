// Package assets embeds the built React SPA (produced by `vite build` into
// internal/assets/dist). Only dist/.gitkeep is committed, so `go build` works
// before the frontend is built; the server then shows a "not built" page.
package assets

import "embed"

//go:embed all:dist
var FS embed.FS
