// Package web embeds the built UI (web/dist, produced by `npm run build`).
package web

import "embed"

// Dist holds the built UI. It contains only a placeholder until the UI is
// built; the server reports that case instead of serving an empty page.
//
//go:embed all:dist
var Dist embed.FS
