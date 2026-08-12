// Package templates embeds the HTML templates so the binary is self-contained.
package templates

import "embed"

//go:embed *.html
var FS embed.FS
