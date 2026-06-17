package main

import (
	"github.com/jmoiron/sqlx"
)

// resetSchema drops every database object and rebuilds an empty `public` schema.
// The caller re-runs migrations afterwards, which recreate the tables AND the
// reference data the migrations seed (units, the default settings row, …) — so
// this is a cleaner wipe than TRUNCATE, which would leave that seeded data gone.
//
// It is pure SQL over the connection, so it works the same against a local
// Postgres container and a hosted database like Neon (no volume to delete).
// Destructive: callers must guard it (see the -reset / -force handling in main).
func resetSchema(db *sqlx.DB) error {
	_, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO public;`)
	return err
}
