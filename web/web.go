// Package web holds the embedded page templates, static assets and the
// llms.txt template.
package web

import "embed"

// FS contains templates/*.html, static/* and llms.txt.
//
//go:embed templates static llms.txt
var FS embed.FS
