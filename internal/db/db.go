// Package db owns the single Postgres connection pool and the Goose migration
// runner. Nothing else in the app opens a database connection.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

// Connect opens and verifies a pooled connection. It returns an error rather
// than calling log.Fatal so the caller controls process lifecycle.
func Connect(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — lets services map a duplicate key to a friendly
// 409 instead of a generic 500.
func IsUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}

// RunMigrations applies all embedded migrations. The migration FS is injected
// (rather than embedded here) because go:embed cannot reference parent
// directories — the embed lives in the migrations package alongside the .sql
// files, which is the correct fix for the original plan's `../../migrations`.
func RunMigrations(sqlDB *sql.DB, migrationsFS fs.FS) error {
	goose.SetBaseFS(migrationsFS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// RunMigrationsTable applies a plugin's embedded migrations using a dedicated
// goose version table (e.g. "goose_db_version_recharge") instead of the core
// "goose_db_version". This keeps a plugin's schema versioning independent of
// core and of every other plugin, which is what makes late-enabling a plugin on
// an existing live database safe: only the plugin's own pending migrations are
// applied; core (and other plugins') schema and data are never touched — no
// wipe. It is idempotent — already-applied migrations are skipped — so it can be
// run on every boot. Any data backfill belongs in the plugin's own migrations
// (idempotent INSERT … SELECT … WHERE NOT EXISTS) or its Setup seeding.
func RunMigrationsTable(sqlDB *sql.DB, migrationsFS fs.FS, table string) error {
	goose.SetBaseFS(migrationsFS)
	defer goose.SetBaseFS(nil)
	// goose's table name is process-global; restore the core default after.
	goose.SetTableName(table)
	defer goose.SetTableName("goose_db_version")
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("goose up (%s): %w", table, err)
	}
	return nil
}
