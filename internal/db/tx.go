package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Queryer is the common surface of *sqlx.DB and *sqlx.Tx that repositories
// depend on. A repository accepts this interface so the same code runs against
// the pool (reads) or inside a transaction (writes) without duplication.
type Queryer interface {
	sqlx.ExtContext
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	NamedExecContext(ctx context.Context, query string, arg any) (sql.Result, error)
	PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error)
}

// Ensure both concrete types satisfy the interface at compile time.
var (
	_ Queryer = (*sqlx.DB)(nil)
	_ Queryer = (*sqlx.Tx)(nil)
)

// WithTx runs fn inside a transaction, rolling back on error or panic and
// committing otherwise.
func WithTx(ctx context.Context, db *sqlx.DB, fn func(tx *sqlx.Tx) error) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
