package db

import (
	"os"
	"testing"
	"testing/fstest"
)

// TestRunMigrationsTable proves the late-enable guarantee: a plugin's migrations
// apply under their own goose version table, are idempotent across reruns, and a
// WHERE-NOT-EXISTS backfill does not duplicate on the second boot. Skipped unless
// DATABASE_URL is set (needs a real Postgres).
func TestRunMigrationsTable(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	drop := func() {
		conn.Exec("DROP TABLE IF EXISTS plugintest_widgets")
		conn.Exec("DROP TABLE IF EXISTS goose_db_version_plugintest")
	}
	drop()
	defer drop()

	fsys := fstest.MapFS{
		"00001_init.sql": {Data: []byte("-- +goose Up\n" +
			"CREATE TABLE plugintest_widgets (id serial primary key, n int);\n" +
			"INSERT INTO plugintest_widgets (n) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM plugintest_widgets);\n" +
			"-- +goose Down\n" +
			"DROP TABLE plugintest_widgets;\n")},
	}

	if err := RunMigrationsTable(conn.DB, fsys, "goose_db_version_plugintest"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := RunMigrationsTable(conn.DB, fsys, "goose_db_version_plugintest"); err != nil {
		t.Fatalf("second run (should be a no-op): %v", err)
	}

	var rows int
	if err := conn.Get(&rows, "SELECT count(*) FROM plugintest_widgets"); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("backfill not idempotent: got %d rows, want 1", rows)
	}

	var hasOwnTable, coreUntouched bool
	conn.Get(&hasOwnTable, "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='goose_db_version_plugintest')")
	if !hasOwnTable {
		t.Fatal("dedicated version table goose_db_version_plugintest not created")
	}
	conn.Get(&coreUntouched, "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='goose_db_version')")
	if !coreUntouched {
		t.Fatal("core goose_db_version table should be untouched/present")
	}
}
