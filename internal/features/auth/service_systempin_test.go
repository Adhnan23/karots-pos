package auth

import (
	"context"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

func systemPINTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}

// The System recovery account's PIN rotates hourly, so it is NOT validated
// against the static pin_hash column. This proves login is decided by the
// injected validator, and that a wrong PIN is still refused.
func TestSystemUserLoginUsesValidator(t *testing.T) {
	db := systemPINTestDB(t)
	defer db.Close()
	ctx := context.Background()

	db.ExecContext(ctx, `DELETE FROM users WHERE phone='0000009999'`) //nolint:errcheck
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (name, phone, role, pin_hash, is_active, must_change_pin, is_system)
		 VALUES ('SysTest', '0000009999', 'admin', 'unusable-hash', true, false, true)`)
	if err != nil {
		t.Fatalf("seed system user: %v", err)
	}
	defer db.ExecContext(ctx, `DELETE FROM users WHERE phone='0000009999'`) //nolint:errcheck

	svc := NewService(db, "0123456789abcdef0123456789abcdef", time.Hour, time.Hour)
	svc.WithSystemPINValidator(func(pin string, _ time.Time) bool { return pin == "424242" })

	if _, err := svc.Login(ctx, LoginInput{Phone: "0000009999", PIN: "wrong"}); err == nil {
		t.Fatal("wrong PIN accepted for system user")
	}
	if _, err := svc.Login(ctx, LoginInput{Phone: "0000009999", PIN: "424242"}); err != nil {
		t.Fatalf("validator-approved PIN rejected: %v", err)
	}
}
