// Package migrations embeds the versioned SQL files so a built binary can
// migrate a database without shipping a separate directory.
package migrations

import "embed"

// FS holds every NNNN_name.sql file in this directory, applied in name order.
//
//go:embed *.sql
var FS embed.FS
