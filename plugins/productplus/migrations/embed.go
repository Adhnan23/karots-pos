// Package migrations embeds the productplus plugin's SQL migrations. They run
// under their own goose version table (goose_db_version_productplus), so enabling
// the plugin on an existing database applies only these — never a core wipe.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
