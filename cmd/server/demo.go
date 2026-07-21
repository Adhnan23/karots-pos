package main

import (
	"context"
	"fmt"
	"time"

	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/purchasereturns"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/sales"

	"github.com/jmoiron/sqlx"
)

// demo builds a transaction-rich shop on top of the entity seed: backdated
// purchases, sales (cash, wholesale, credit), a customer repayment, returns,
// cash register sessions and expenses. Unlike seed (entities only) it is meant
// for demos and manual testing where every report/page should show data.
//
// It runs everything through the normal feature services (so the data is exactly
// what the app itself would produce) and then backdates the rows so trend charts
// and date-range reports look populated. It is idempotent: it skips if any sale
// already exists.
func demo(db *sqlx.DB) error {
	ctx := context.Background()

	var saleCount int
	if err := db.GetContext(ctx, &saleCount, `SELECT COUNT(*) FROM sales`); err != nil {
		return err
	}
	if saleCount > 0 {
		fmt.Println("demo data already present; skipping")
		return nil
	}

	// Ensure the entities exist first (self-skips if already seeded).
	if err := seed(db); err != nil {
		return err
	}

	adminID := userIDByPhone(db, "0771234567")   // Admin — back-office actions
	cashierID := userIDByPhone(db, "0771111111") // Cashier — rings up sales
	if adminID == 0 || cashierID == 0 {
		return fmt.Errorf("demo: expected seeded users not found")
	}

	prods, err := loadProducts(db)
	if err != nil {
		return err
	}
	sup := func(name string) int64 { return idByName(db, "suppliers", name) }
	cust := func(name string) int64 { return idByName(db, "customers", name) }

	now := time.Now()
	// day returns a timestamp n days ago at ~13:00 local for tidy report buckets.
	day := func(n, hour int) time.Time {
		d := now.AddDate(0, 0, -n)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 30, 0, 0, d.Location())
	}
	ymd := func(n int) string { return now.AddDate(0, 0, -n).Format("2006-01-02") }

	purchaseSvc := purchases.NewService(db)
	saleSvc := sales.NewService(db)
	expenseSvc := expenses.NewService(db)
	custSvc := customers.NewService(db)
	purRetSvc := purchasereturns.NewService(db)
	regSvc := cashregister.NewService(db, saleSvc).WithAudit(audit.NewService(db))

	// ---- Purchases (restock well above opening, create supplier dues) ----
	expiry := func(n int) string { return now.AddDate(0, 0, n).Format("2006-01-02") }
	type pin = purchases.ItemInput
	// Demo purchases are left unpaid: paying a supplier now means a real payment
	// row plus cash leaving a locker or till (see supplierpay + cashflow), and
	// the seeder has neither. Unpaid is also the more useful demo — it gives the
	// supplier-dues screens something to show.
	mkPurchase := func(supplier string, daysAgo int, items []pin) error {
		inv := fmt.Sprintf("INV-%d", 1000+daysAgo)
		d, err := purchaseSvc.Create(ctx, purchases.CreateInput{
			SupplierID: sup(supplier),
			InvoiceNo:  &inv,
			Items:      items,
		}, adminID)
		if err != nil {
			return fmt.Errorf("demo purchase from %s: %w", supplier, err)
		}
		return touch(db, `UPDATE purchases SET created_at=$1 WHERE id=$2`, day(daysAgo, 9), d.Purchase.ID)
	}
	if err := mkPurchase("Colombo Distributors (Pvt) Ltd", 12, []pin{
		{ProductID: prods["1000000000017"], Quantity: "50", CostPrice: "230", SellingPrice: "250"}, // Sugar
		{ProductID: prods["1000000000024"], Quantity: "60", CostPrice: "210", SellingPrice: "240"}, // Rice Samba
		{ProductID: prods["1000000000031"], Quantity: "40", CostPrice: "120", SellingPrice: "150"}, // Tea
	}); err != nil {
		return err
	}
	if err := mkPurchase("Kandy Wholesale Traders", 9, []pin{
		{ProductID: prods["1000000000062"], Quantity: "40", CostPrice: "300", SellingPrice: "340"}, // Dhal
		{ProductID: prods["1000000000079"], Quantity: "40", CostPrice: "260", SellingPrice: "300"}, // Red Rice
	}); err != nil {
		return err
	}
	if err := mkPurchase("Lanka Beverages Agency", 8, []pin{
		{ProductID: prods["1000000000048"], Quantity: "36", CostPrice: "320", SellingPrice: "380", ExpiryDate: expiry(120)}, // Coca-Cola
		{ProductID: prods["1000000000086"], Quantity: "30", CostPrice: "260", SellingPrice: "310", ExpiryDate: expiry(120)}, // Sprite
		{ProductID: prods["1000000000055"], Quantity: "60", CostPrice: "60", SellingPrice: "90"},                            // Mineral Water
	}); err != nil {
		return err
	}

	// ---- A closed prior cash register session (with a pay-in) ----
	if sess, err := regSvc.Open(ctx, cashierID, cashregister.OpenInput{OpeningCash: "10000"}); err != nil {
		return fmt.Errorf("demo open register A: %w", err)
	} else {
		if _, err := regSvc.PayIn(ctx, cashierID, cashregister.MovementInput{Amount: "2000", Reason: "extra cash"}); err != nil {
			return fmt.Errorf("demo pay-in: %w", err)
		}
		if _, err := regSvc.Close(ctx, cashierID, cashregister.CloseInput{ClosingCash: "12000"}); err != nil {
			return fmt.Errorf("demo close register A: %w", err)
		}
		if err := touch(db, `UPDATE cash_register SET opened_at=$1, closed_at=$2 WHERE id=$3`, day(6, 9), day(6, 18), sess.ID); err != nil {
			return err
		}
	}

	// ---- Sales (backdated; populate reports, dues, discounts) ----
	cashPay := func(amount string) []sales.PaymentInput {
		return []sales.PaymentInput{{Method: "cash", Amount: amount}}
	}
	type sitem = sales.ItemInput
	mkSale := func(in sales.CreateInput, daysAgo, hour int) (*sales.Detail, error) {
		d, err := saleSvc.Create(ctx, in, cashierID)
		if err != nil {
			return nil, err
		}
		when := day(daysAgo, hour)
		if err := touch(db, `UPDATE sales SET created_at=$1 WHERE id=$2`, when, d.Sale.ID); err != nil {
			return nil, err
		}
		if err := touch(db, `UPDATE payments SET created_at=$1 WHERE sale_id=$2`, when, d.Sale.ID); err != nil {
			return nil, err
		}
		return d, nil
	}

	// 1) retail cash — becomes the sale we partially return later.
	sale1, err := mkSale(sales.CreateInput{
		SaleType: "retail",
		Items: []sitem{
			{ProductID: prods["1000000000017"], Quantity: "2"},
			{ProductID: prods["1000000000031"], Quantity: "1"},
			{ProductID: prods["1000000000048"], Quantity: "2"},
		},
		Payments: cashPay("1500"),
	}, 6, 10)
	if err != nil {
		return fmt.Errorf("demo sale 1: %w", err)
	}
	// 2) retail cash with a per-line percent discount.
	if _, err := mkSale(sales.CreateInput{
		SaleType: "retail",
		Items: []sitem{
			{ProductID: prods["1000000000024"], Quantity: "3", Discount: "10", DiscountType: "percent"},
			{ProductID: prods["1000000000055"], Quantity: "4"},
		},
		Payments: cashPay("1200"),
	}, 5, 11); err != nil {
		return fmt.Errorf("demo sale 2: %w", err)
	}
	// 3) wholesale cash.
	if _, err := mkSale(sales.CreateInput{
		SaleType: "wholesale",
		Items:    []sitem{{ProductID: prods["1000000000062"], Quantity: "2"}},
		Payments: cashPay("700"),
	}, 5, 15); err != nil {
		return fmt.Errorf("demo sale 3: %w", err)
	}
	// 4) retail cash with a bill-level percent discount.
	if _, err := mkSale(sales.CreateInput{
		SaleType:     "retail",
		Discount:     "5",
		DiscountType: "percent",
		Items: []sitem{
			{ProductID: prods["1000000000048"], Quantity: "3"},
			{ProductID: prods["1000000000086"], Quantity: "2"},
		},
		Payments: cashPay("2000"),
	}, 4, 12); err != nil {
		return fmt.Errorf("demo sale 4: %w", err)
	}
	// 5) retail cash.
	if _, err := mkSale(sales.CreateInput{
		SaleType: "retail",
		Items: []sitem{
			{ProductID: prods["1000000000079"], Quantity: "2"},
			{ProductID: prods["1000000000017"], Quantity: "1"},
		},
		Payments: cashPay("900"),
	}, 3, 14); err != nil {
		return fmt.Errorf("demo sale 5: %w", err)
	}
	// 6) retail cash.
	if _, err := mkSale(sales.CreateInput{
		SaleType: "retail",
		Items: []sitem{
			{ProductID: prods["1000000000031"], Quantity: "2"},
			{ProductID: prods["1000000000055"], Quantity: "3"},
		},
		Payments: cashPay("600"),
	}, 2, 9); err != nil {
		return fmt.Errorf("demo sale 6: %w", err)
	}
	// 7) credit sale, no payment — full balance on credit (Nimal Perera).
	nimal := cust("Nimal Perera")
	if _, err := mkSale(sales.CreateInput{
		CustomerID: &nimal,
		SaleType:   "credit",
		Items: []sitem{
			{ProductID: prods["1000000000024"], Quantity: "4"},
			{ProductID: prods["1000000000062"], Quantity: "2"},
		},
	}, 2, 16); err != nil {
		return fmt.Errorf("demo sale 7 (credit): %w", err)
	}
	// 8) credit sale, partial cash payment (Kamala Stores).
	kamala := cust("Kamala Stores")
	if _, err := mkSale(sales.CreateInput{
		CustomerID: &kamala,
		SaleType:   "credit",
		Items: []sitem{
			{ProductID: prods["1000000000048"], Quantity: "6"},
			{ProductID: prods["1000000000086"], Quantity: "4"},
		},
		Payments: cashPay("1500"),
	}, 1, 10); err != nil {
		return fmt.Errorf("demo sale 8 (credit): %w", err)
	}

	// ---- Customer repayment (Nimal) — exercises the statement ledger ----
	if err := custSvc.RecordPayment(ctx, nimal, customers.PaymentInput{Amount: "1000", Method: "cash"}, adminID); err != nil {
		return fmt.Errorf("demo customer payment: %w", err)
	}
	if err := touch(db, `UPDATE customer_payments SET created_at=$1 WHERE customer_id=$2`, day(1, 12), nimal); err != nil {
		return err
	}

	// ---- Returns ----
	// Sale return: send back 1 unit of the first line of sale 1.
	if _, _, err := saleSvc.PartialReturn(ctx, sale1.Sale.ID, sales.PartialReturnInput{
		Lines: []sales.ReturnLineInput{{SaleItemID: sale1.Items[0].ID, Quantity: "1"}},
	}, adminID); err != nil {
		return fmt.Errorf("demo sale return: %w", err)
	}
	if err := touch(db, `UPDATE sale_returns SET created_at=$1 WHERE sale_id=$2`, day(5, 12), sale1.Sale.ID); err != nil {
		return err
	}
	// Purchase return: send 5 Mineral Water back to the beverage supplier.
	ret := "DN-1001"
	if _, err := purRetSvc.Create(ctx, purchasereturns.CreateInput{
		SupplierID: sup("Lanka Beverages Agency"),
		Reference:  &ret,
		Items:      []purchasereturns.ItemInput{{ProductID: prods["1000000000055"], Quantity: "5", CostPrice: "60"}},
	}, adminID); err != nil {
		return fmt.Errorf("demo purchase return: %w", err)
	}
	if err := touch(db, `UPDATE purchase_returns SET created_at=$1 WHERE id=(SELECT MAX(id) FROM purchase_returns)`, day(1, 13)); err != nil {
		return err
	}

	// ---- A second closed session (counted short) ----
	if sess, err := regSvc.Open(ctx, cashierID, cashregister.OpenInput{OpeningCash: "10000"}); err != nil {
		return fmt.Errorf("demo open register B: %w", err)
	} else {
		if _, err := regSvc.Close(ctx, cashierID, cashregister.CloseInput{ClosingCash: "9800"}); err != nil {
			return fmt.Errorf("demo close register B: %w", err)
		}
		if err := touch(db, `UPDATE cash_register SET opened_at=$1, closed_at=$2 WHERE id=$3`, day(3, 9), day(3, 18), sess.ID); err != nil {
			return err
		}
	}

	// ---- Today's open session + two live (non-backdated) cash sales ----
	if _, err := regSvc.Open(ctx, cashierID, cashregister.OpenInput{OpeningCash: "10000"}); err != nil {
		return fmt.Errorf("demo open register today: %w", err)
	}
	if _, err := saleSvc.Create(ctx, sales.CreateInput{
		SaleType: "retail",
		Items: []sitem{
			{ProductID: prods["1000000000017"], Quantity: "2"},
			{ProductID: prods["1000000000055"], Quantity: "2"},
		},
		Payments: cashPay("700"),
	}, cashierID); err != nil {
		return fmt.Errorf("demo today sale 1: %w", err)
	}
	if _, err := saleSvc.Create(ctx, sales.CreateInput{
		SaleType: "retail",
		Items: []sitem{
			{ProductID: prods["1000000000031"], Quantity: "3"},
			{ProductID: prods["1000000000086"], Quantity: "1"},
		},
		Payments: cashPay("800"),
	}, cashierID); err != nil {
		return fmt.Errorf("demo today sale 2: %w", err)
	}

	// ---- Expenses (backdated natively via ExpenseDate) ----
	descr := func(s string) *string { return &s }
	exps := []struct {
		cat, amt, note string
		daysAgo        int
	}{
		{"Rent", "25000", "Monthly shop rent", 12},
		{"Salaries", "45000", "Staff wages", 10},
		{"Electricity", "8500", "CEB bill", 7},
		{"Transport", "3200", "Goods delivery", 4},
		{"Cleaning", "1500", "Supplies", 2},
	}
	for _, e := range exps {
		if _, err := expenseSvc.Create(ctx, expenses.CreateInput{
			Category:    e.cat,
			Amount:      e.amt,
			Description: descr(e.note),
			ExpenseDate: ymd(e.daysAgo),
		}, adminID); err != nil {
			return fmt.Errorf("demo expense %s: %w", e.cat, err)
		}
	}

	fmt.Println("demo seeded: 3 purchases, 10 sales (8 backdated + 2 today, incl. 2 credit + discounts),")
	fmt.Println("             1 customer repayment, 1 sale return, 1 purchase return,")
	fmt.Println("             3 cash register sessions (2 closed + 1 open), 5 expenses.")
	fmt.Println("Sign in as Admin / 1234 (cashier 0771111111 / 1111) to explore.")
	return nil
}

// loadProducts maps each product's barcode to its id so the demo can reference
// products by their stable barcode rather than assuming serial ids.
func loadProducts(db *sqlx.DB) (map[string]int64, error) {
	rows, err := db.Query(`SELECT barcode, id FROM products WHERE barcode IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var bc string
		var id int64
		if err := rows.Scan(&bc, &id); err != nil {
			return nil, err
		}
		out[bc] = id
	}
	return out, rows.Err()
}

func userIDByPhone(db *sqlx.DB, phone string) int64 {
	var id int64
	_ = db.Get(&id, `SELECT id FROM users WHERE phone = $1`, phone)
	return id
}

// idByName looks up a row id by its name column in the given table (suppliers,
// customers). Used so the demo doesn't assume serial ids.
func idByName(db *sqlx.DB, table, name string) int64 {
	var id int64
	_ = db.Get(&id, `SELECT id FROM `+table+` WHERE name = $1`, name)
	return id
}

// touch runs a single UPDATE used to backdate a freshly created row so the demo
// data is spread across recent days.
func touch(db *sqlx.DB, query string, args ...any) error {
	_, err := db.Exec(query, args...)
	return err
}
