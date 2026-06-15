package main

import (
	"fmt"
	"os"

	"github.com/jmoiron/sqlx"
)

// initShop prepares a brand-new shop for first use. A fresh shop ships with NO
// staff accounts — only the hidden system recovery admin (see ensureSystemAdmin).
// You sign in with that account and create the shop's real users yourself in
// Admin → Users. Migrations have already run by the time this is called; init
// just makes sure the system account exists so the install is immediately usable.
//
// It leaves the catalog empty and the shop identity at its neutral migration
// default ("My Shop") so the owner configures everything in the UI. It is
// idempotent and safe to run on every deploy.
func initShop(db *sqlx.DB) error {
	if err := ensureSystemAdmin(db); err != nil {
		return err
	}
	fmt.Println("init complete: empty shop ready.")
	fmt.Println("Sign in with your system recovery account, then create the shop's users in Admin → Users.")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
