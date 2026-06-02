// Package migrations embeds the Goose SQL migration files into the binary.
// The embed lives here, alongside the .sql files, because go:embed cannot
// reference parent directories — this is the correct fix for the original
// plan's invalid `//go:embed ../../migrations/*.sql`.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
