// Package web embeds the admin UI's templates and static assets into the
// swarmdash binary so it ships as a single file with no external assets.
package web

import (
	"embed"
	"html/template"
	"io/fs"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// StaticFS is rooted at the "static" directory itself, so it can be served
// directly under the /static/ URL prefix.
var StaticFS = mustSub(staticFS, "static")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// Templates parses every embedded page template alongside the shared
// layout, keyed by file name (e.g. "dashboard.html").
func Templates() *template.Template {
	return template.Must(template.New("").Funcs(FuncMap).ParseFS(templatesFS, "templates/*.html"))
}
