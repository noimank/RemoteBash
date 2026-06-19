// Package web provides embedded template and static asset access.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed static/*
var StaticFS embed.FS

//go:embed templates/base.gohtml templates/partials/*.gohtml
var templateFS embed.FS

// ParseTemplates loads and parses all Go html/template files.
func ParseTemplates() (*template.Template, error) {
	return template.ParseFS(templateFS, "templates/base.gohtml", "templates/partials/*.gohtml")
}

// StaticHTTPFS returns an fs.FS rooted at static/ for HTTP serving.
// The returned FS strips the "static/" prefix.
func StaticHTTPFS() (http.FileSystem, error) {
	sub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		return nil, err
	}
	return http.FS(sub), nil
}
