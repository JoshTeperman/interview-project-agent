// Package web embeds the browser UI into the binary (S-D12).
// Nothing here contains project data; the snapshot is only reachable through authenticated API routes.
package web

import "embed"

// FS holds the HTML pages and the static/ assets.
//
//go:embed *.html static
var FS embed.FS
