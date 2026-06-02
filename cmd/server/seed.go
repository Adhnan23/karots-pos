package main

import (
	"context"
	"fmt"

	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/stock"

	"github.com/jmoiron/sqlx"
)

// seed inserts a usable starter dataset: an admin + cashier, a couple of
// categories, and a handful of stocked products. It is idempotent enough for
// dev use — it skips seeding if any users already exist.
func seed(db *sqlx.DB) error {
	ctx := context.Background()

	var userCount int
	if err := db.GetContext(ctx, &userCount, `SELECT COUNT(*) FROM users`); err != nil {
		return err
	}
	if userCount > 0 {
		fmt.Println("users already exist; skipping seed")
		return nil
	}

	authSvc := auth.NewService(db, "seed-secret-not-used-for-signing-only", 0, 0)
	if _, err := authSvc.CreateUser(ctx, auth.CreateUserInput{
		Name: "Admin", Phone: "0771234567", Role: auth.RoleAdmin, PIN: "1234",
	}); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if _, err := authSvc.CreateUser(ctx, auth.CreateUserInput{
		Name: "Cashier", Phone: "0771111111", Role: auth.RoleCashier, PIN: "1111",
	}); err != nil {
		return fmt.Errorf("seed cashier: %w", err)
	}

	catSvc := categories.NewService(db)
	groceries, err := catSvc.Create(ctx, categories.CreateInput{Name: "Groceries"})
	if err != nil {
		return fmt.Errorf("seed category: %w", err)
	}
	beverages, err := catSvc.Create(ctx, categories.CreateInput{Name: "Beverages"})
	if err != nil {
		return fmt.Errorf("seed category: %w", err)
	}

	// Unit ids: the migration seeds units in a known order; look up by abbr.
	pcs := unitID(db, "pcs")
	kg := unitID(db, "kg")

	prodSvc := products.NewService(db)
	stockSvc := stock.NewService(db)
	adminID := int64(1)

	type p struct {
		name, barcode string
		cat, unit     int64
		cost, sell    string
		qty           string
	}
	items := []p{
		{"Sugar 1kg", "1000000000017", groceries.ID, kg, "230", "250", "40"},
		{"Rice 1kg (Samba)", "1000000000024", groceries.ID, kg, "210", "240", "60"},
		{"Tea Leaves 100g", "1000000000031", groceries.ID, pcs, "120", "150", "30"},
		{"Coca-Cola 1.5L", "1000000000048", beverages.ID, pcs, "320", "380", "24"},
		{"Mineral Water 1L", "1000000000055", beverages.ID, pcs, "60", "90", "50"},
	}
	for _, it := range items {
		bc := it.barcode
		created, err := prodSvc.Create(ctx, products.CreateInput{
			Name:         it.name,
			Barcode:      &bc,
			CategoryID:   it.cat,
			UnitID:       it.unit,
			CostPrice:    it.cost,
			SellingPrice: it.sell,
			ReorderLevel: 10,
		})
		if err != nil {
			return fmt.Errorf("seed product %s: %w", it.name, err)
		}
		if err := stockSvc.Adjust(ctx, stock.AdjustInput{
			ProductID:   created.ID,
			NewQuantity: it.qty,
			Note:        "opening stock",
		}, adminID); err != nil {
			return fmt.Errorf("seed stock %s: %w", it.name, err)
		}
	}

	fmt.Println("seeded: Admin (PIN 1234), Cashier (PIN 1111), 2 categories, 5 products")
	return nil
}

func unitID(db *sqlx.DB, abbr string) int64 {
	var id int64
	_ = db.Get(&id, `SELECT id FROM units WHERE abbreviation = $1`, abbr)
	return id
}
