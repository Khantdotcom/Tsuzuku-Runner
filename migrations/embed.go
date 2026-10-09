// Package migrations embeds the goose SQL migrations that define the schema.
package migrations

import "embed"

// FS holds every migration file, embedded into binaries that apply them.
//
//go:embed *.sql
var FS embed.FS
