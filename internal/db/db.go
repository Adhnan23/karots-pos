// Package db owns the single Postgres connection pool and the Goose migration
// runner. Nothing else in the app opens a database connection.
package db

import (
	"database/sql"
	"fmt"
	"io/fs"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
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
