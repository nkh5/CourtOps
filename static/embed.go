// Package static embeds the stylesheet, club mark and board's polling script.
package static

import "embed"

//go:embed *.css *.js *.svg
var FS embed.FS
