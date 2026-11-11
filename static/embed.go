// Package static embeds the stylesheet, club mark and board's polling script.
package static

import "embed"

//go:embed *.css *.js *.mjs *.svg
var FS embed.FS
