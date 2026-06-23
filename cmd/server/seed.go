package main

import (
	"context"
	"fmt"

	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/suppliers"

	"github.com/jmoiron/sqlx"
)

// seed inserts a usable starter dataset — staff users, the shop's identity, a
// nested category tree, stocked products, suppliers and customers. It is
// idempotent for dev use: it skips entirely if any users already exist, so a
// clean run wants a fresh database (see "Reset the database" in the README).
//
// It deliberately seeds ENTITIES ONLY (no sample sales/purchases) so the
// dashboard and reports start from a clean, zero-transaction state.
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

	// --- staff users (phone + PIN login) ---
	authSvc := auth.NewService(db, "seed-secret-not-used-for-signing-only", 0, 0)
	staff := []struct {
		name, phone, pin string
		role             string
	}{
		{"Admin", "0771234567", "1234", auth.RoleAdmin},
		{"Manager", "0772222222", "2222", auth.RoleManager},
		{"Cashier", "0771111111", "1111", auth.RoleCashier},
	}
	for _, u := range staff {
		if _, err := authSvc.CreateUser(ctx, auth.CreateUserInput{
			Name: u.name, Phone: u.phone, Role: u.role, PIN: u.pin,
		}); err != nil {
			return fmt.Errorf("seed user %s: %w", u.name, err)
		}
	}

	// --- shop identity (preserve the migration defaults for everything else) ---
	setSvc := settings.NewService(db)
	cur, err := setSvc.Get(ctx)
	if err != nil {
		return fmt.Errorf("seed settings load: %w", err)
	}
	shopNameSi := "කරොට්ස් සුපර්මාර්කට්"
	address := "123 Galle Road, Colombo 03"
	phone := "0112345678"
	footer := "Thank you! Please come again."
	if _, err := setSvc.Update(ctx, settings.UpdateInput{
		ShopName:        "Karots Super Mart",
		ShopNameSi:      &shopNameSi, // prints on the thermal receipt as a raster image
		Address:         &address,
		Phone:           &phone,
		CurrencyCode:    cur.CurrencyCode,
		CurrencySymbol:  cur.CurrencySymbol,
		ReceiptFooter:   &footer,
		ReceiptWidth:    cur.ReceiptWidth,
		TaxRegistered:   cur.TaxRegistered,
		LowStockAlerts:  cur.LowStockAlerts,
		PromptAfterSale: cur.PromptAfterSale,
		ForcePinChange:        cur.ForcePinChange,
		AllowCashierPinChange: cur.AllowCashierPinChange,
		DefaultSaleType: cur.DefaultSaleType,
		ReceiptPrinter:  cur.ReceiptPrinter,
		LabelPrinter:    cur.LabelPrinter,
		LabelWidthMM:    cur.LabelWidthMM,
		LabelHeightMM:   cur.LabelHeightMM,
		LabelGapMM:      cur.LabelGapMM,
	}); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}

	// --- nested categories (parent → child, to exercise nested filtering) ---
	catSvc := categories.NewService(db)
	mkCat := func(name string, parent *int64) (int64, error) {
		c, err := catSvc.Create(ctx, categories.CreateInput{Name: name, ParentID: parent})
		if err != nil {
			return 0, fmt.Errorf("seed category %s: %w", name, err)
		}
		return c.ID, nil
	}
	groceries, err := mkCat("Groceries", nil)
	if err != nil {
		return err
	}
	riceGrains, err := mkCat("Rice & Grains", &groceries)
	if err != nil {
		return err
	}
	beverages, err := mkCat("Beverages", nil)
	if err != nil {
		return err
	}
	softDrinks, err := mkCat("Soft Drinks", &beverages)
	if err != nil {
		return err
	}

	// Unit ids: the migration seeds units in a known order; look up by abbr.
	pcs := unitID(db, "pcs")
	kg := unitID(db, "kg")

	// --- products + opening stock ---
	prodSvc := products.NewService(db)
	stockSvc := stock.NewService(db)
	adminID := int64(1)

	type p struct {
		name, nameSi, barcode      string
		cat, unit                  int64
		cost, sell, wholesale, tax string
		reorder                    int
		qty                        string
		pin                        bool // surface on the cashier default grid
	}
	items := []p{
		{"Sugar 1kg", "", "1000000000017", groceries, kg, "230", "250", "", "", 10, "40", true},
		{"Tea Leaves 100g", "තේ කොළ 100g", "1000000000031", groceries, pcs, "120", "150", "", "", 10, "30", true},
		{"Dhal (Mysoor) 1kg", "", "1000000000062", groceries, kg, "300", "340", "320", "", 8, "35", false},
		{"Rice 1kg (Samba)", "", "1000000000024", riceGrains, kg, "210", "240", "225", "", 15, "60", true},
		{"Red Rice 1kg", "", "1000000000079", riceGrains, kg, "260", "300", "280", "", 15, "45", false},
		{"Coca-Cola 1.5L", "", "1000000000048", softDrinks, pcs, "320", "380", "", "", 10, "24", true},
		{"Sprite 1L", "", "1000000000086", softDrinks, pcs, "260", "310", "", "", 10, "18", false},
		{"Mineral Water 1L", "", "1000000000055", beverages, pcs, "60", "90", "", "", 20, "50", true},
	}
	for _, it := range items {
		bc := it.barcode
		in := products.CreateInput{
			Name:           it.name,
			Barcode:        &bc,
			CategoryID:     it.cat,
			UnitID:         it.unit,
			CostPrice:      it.cost,
			SellingPrice:   it.sell,
			WholesalePrice: it.wholesale,
			TaxRate:        it.tax,
			ReorderLevel:   it.reorder,
			IsPinned:       it.pin,
		}
		if it.nameSi != "" {
			ns := it.nameSi
			in.NameLocal = &ns
		}
		created, err := prodSvc.Create(ctx, in)
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

	// --- suppliers ---
	supSvc := suppliers.NewService(db)
	strptr := func(s string) *string { return &s }
	supItems := []suppliers.CreateInput{
		{Name: "Colombo Distributors (Pvt) Ltd", ContactPerson: strptr("Ravi Jayasuriya"), Phone: strptr("0114567890"), Address: strptr("45 Sea Street, Colombo 11"), CreditDays: 30},
		{Name: "Kandy Wholesale Traders", ContactPerson: strptr("Anil Bandara"), Phone: strptr("0812233445"), Address: strptr("12 Peradeniya Road, Kandy"), CreditDays: 14},
		{Name: "Lanka Beverages Agency", ContactPerson: strptr("Shanika Mendis"), Phone: strptr("0119988776"), CreditDays: 7},
	}
	for _, s := range supItems {
		if _, err := supSvc.Create(ctx, s); err != nil {
			return fmt.Errorf("seed supplier %s: %w", s.Name, err)
		}
	}

	// --- customers (one walk-in with no credit, two with limits) ---
	custSvc := customers.NewService(db)
	custItems := []customers.CreateInput{
		{Name: "Nimal Perera", Phone: strptr("0771239876"), Address: strptr("8 Temple Road, Nugegoda"), CreditLimit: "5000"},
		{Name: "Kamala Stores", Phone: strptr("0775554433"), Address: strptr("Main Street, Maharagama"), CreditLimit: "20000"},
		{Name: "Sunil Fernando", Phone: strptr("0712221110"), CreditLimit: "0"},
	}
	for _, c := range custItems {
		if _, err := custSvc.Create(ctx, c); err != nil {
			return fmt.Errorf("seed customer %s: %w", c.Name, err)
		}
	}

	fmt.Println("seeded: 3 users (Admin/1234, Manager/2222, Cashier/1111), shop settings,")
	fmt.Println("        4 categories (2 nested), 8 products with opening stock, 3 suppliers, 3 customers")
	return nil
}

func unitID(db *sqlx.DB, abbr string) int64 {
	var id int64
	_ = db.Get(&id, `SELECT id FROM units WHERE abbreviation = $1`, abbr)
	return id
}
