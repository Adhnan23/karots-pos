package plugin

import (
	"karots-pos/internal/config"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/lockers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"

	"github.com/jmoiron/sqlx"
)

// Core is the frozen API surface a plugin may use: the database, the loaded
// config, and a curated set of core services. Plugins depend only on this (and
// the Registry) — never on internal/web — so the UI layer can evolve freely.
//
// The service instances here are the SAME ones the core UI uses (built once in
// the web layer), so a plugin sale, expense or audit entry is identical to a
// core one.
type Core struct {
	DB  *sqlx.DB
	Cfg *config.Config

	Audit        *audit.Service
	Settings     *settings.Service
	CashRegister *cashregister.Service
	Sales        *sales.Service
	Expenses     *expenses.Service
	Products     *products.Service
	Stock        *stock.Service
	// Cashflow + Lockers let a plugin move money between real cash locations
	// (lockers/tills) and list them for a source picker — e.g. paying an outside
	// repairer from a chosen locker, exactly like the core expense/bill-pay flow.
	Cashflow *cashflow.Service
	Lockers  *lockers.Service
}
