// Package web holds the embedded page templates, static assets and the
// llms.txt template.
package web

import "embed"

// FS contains templates/*.html, static/*, demo/*.json and llms.txt.
//
//go:embed templates static demo llms.txt
var FS embed.FS
