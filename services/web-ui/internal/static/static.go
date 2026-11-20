// Package static embeds the built SPA (web/ → `npm run build` → dist/).
package static

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the built frontend. dist/ holds only .keep until the frontend is built.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
