// Package assets embeds the built React SPA (produced by `vite build` into
// internal/assets/dist). The committed placeholder lets `go build` succeed
// before the frontend is built; a real build overwrites it.
package assets

import "embed"

//go:embed all:dist
var FS embed.FS
