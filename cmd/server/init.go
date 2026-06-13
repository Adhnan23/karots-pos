package main

import (
	"context"
	"fmt"
	"os"

	"karots-pos/internal/features/auth"

	"github.com/jmoiron/sqlx"
)

// initShop prepares a brand-new shop for first use: it creates a single admin
// account and nothing else. Unlike seed (which loads demo categories, products,
// suppliers and customers for development), init leaves the catalog empty and
// the shop identity at its neutral migration default ("My Shop") so the owner
// configures everything in the UI.
//
// It is idempotent: if any users already exist it does nothing, so it is safe to
// run on every deploy. The admin's PIN is flagged must_change (handled by
// CreateUser), so the owner is forced to choose their own PIN on first login.
//
// The default credentials can be overridden via environment variables so a
// fresh install need not ship with a well-known PIN:
//
//	POS_ADMIN_NAME, POS_ADMIN_PHONE, POS_ADMIN_PIN
func initShop(db *sqlx.DB) error {
	ctx := context.Background()

	var userCount int
	if err := db.GetContext(ctx, &userCount, `SELECT COUNT(*) FROM users`); err != nil {
		return err
	}
	if userCount > 0 {
		fmt.Println("init: users already exist; nothing to do")
		return nil
	}

	name := envOr("POS_ADMIN_NAME", "Owner")
	phone := envOr("POS_ADMIN_PHONE", "0771234567")
	pin := envOr("POS_ADMIN_PIN", "1234")

	authSvc := auth.NewService(db, "init-secret-not-used-for-signing-only", 0, 0)
	if _, err := authSvc.CreateUser(ctx, auth.CreateUserInput{
		Name: name, Phone: phone, Role: auth.RoleAdmin, PIN: pin,
	}); err != nil {
		return fmt.Errorf("init admin: %w", err)
	}

	fmt.Printf("init complete: admin %q created (phone %s, PIN %s).\n", name, phone, pin)
	fmt.Println("You will be asked to choose a new PIN on first login.")
	fmt.Println("Next: open the app, log in, then set your shop name, units and products in Settings.")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
