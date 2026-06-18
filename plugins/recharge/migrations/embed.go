// Package migrations embeds the recharge plugin's SQL migrations. They run under
// their own goose version table (goose_db_version_recharge), so enabling the
// plugin on an existing database applies only these — never a core wipe.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
