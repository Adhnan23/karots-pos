// Package web hosts the server-rendered UI (HTMX + Templ). It imports feature
// services and template packages; feature packages never import templates, so
// there is no import cycle. This is the structural fix for the original plan,
// where ui_handler.go lived inside each feature and imported the templates that
// in turn imported the feature — an illegal cycle in Go.
package web

import (
	"net/http"

	"karots-pos/internal/config"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/conversions"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/denominations"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/purchasereturns"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/supplierpay"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/features/units"
	"karots-pos/internal/middleware"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// Server bundles the services the UI handlers call.
type Server struct {
	cfg        *config.Config
	auth       *auth.Service
	products   *products.Service
	categories *categories.Service
	units      *units.Service
	settings   *settings.Service
	sales      *sales.Service
	customers  *customers.Service
	stock      *stock.Service
	suppliers  *suppliers.Service
	supplierPay *supplierpay.Service
	purchases  *purchases.Service
	purchaseReturns *purchasereturns.Service
	conversions *conversions.Service
	expenses   *expenses.Service
	reports    *reports.Service
	denominations *denominations.Service
	cashRegister  *cashregister.Service
	audit         *audit.Service
}

// RegisterUI builds UI services and mounts all server-rendered routes. authSvc
// is shared with the API layer so login state is consistent.
func RegisterUI(e *echo.Echo, db *sqlx.DB, cfg *config.Config, authSvc *auth.Service) {
	s := &Server{
		cfg:        cfg,
		auth:       authSvc,
		products:   products.NewService(db),
		categories: categories.NewService(db),
		units:      units.NewService(db),
		settings:   settings.NewService(db),
		sales:      sales.NewService(db),
		customers:  customers.NewService(db),
		stock:      stock.NewService(db),
		suppliers:  suppliers.NewService(db),
		supplierPay: supplierpay.NewService(db),
		purchases:  purchases.NewService(db),
		purchaseReturns: purchasereturns.NewService(db),
		conversions: conversions.NewService(db),
		expenses:   expenses.NewService(db),
		reports:    reports.NewService(db),
		denominations: denominations.NewService(db),
		audit:         audit.NewService(db),
	}
	s.cashRegister = cashregister.NewService(db, sales.NewService(db)).WithAudit(s.audit)
	a := &authUI{svc: authSvc, cookie: CookieConfig{Secure: cfg.CookieSecure, MaxAge: cfg.JWTAccessTTL}}
	admin := &adminUI{s: s, db: db}
	cashier := &cashierUI{s: s}

	limiter := auth.NewLoginLimiter()
	jwt := middleware.JWTAuth(cfg.JWTSecret)

	// Public
	e.GET("/login", a.ShowLogin)
	e.POST("/login", a.Login, limiter)
	e.POST("/logout", a.Logout)

	// Root: send the user to their home by role.
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusSeeOther, auth.HomePath(middleware.CurrentRole(c)))
	}, jwt)

	// Cashier (all authenticated roles)
	cg := e.Group("/cashier", jwt)
	cg.GET("", cashier.POS)
	cg.GET("/receipt/:id", cashier.Receipt)
	cg.POST("/print/:id", cashier.PrintReceipt)
	cg.GET("/receipts", cashier.Receipts)
	cg.GET("/labels", cashier.Labels)
	cg.POST("/labels/send", cashier.LabelsSend)
	cg.GET("/z/:id", cashier.ZReport) // day-end (Z) report — own session

	// Returns / refunds
	cg.GET("/returns", cashier.Returns)
	cg.GET("/returns/table", cashier.ReturnsTable)
	cg.GET("/returns/:id", cashier.ReturnForm)
	cg.POST("/sales/:id/partial-return", cashier.ReturnSubmit)

	// Damage / write-off
	cg.GET("/damage", cashier.Damage)
	cg.POST("/damage", cashier.DamageRecord)

	// Credit collection
	cg.GET("/credit", cashier.Credit)
	cg.GET("/credit/table", cashier.CreditTable)
	cg.GET("/credit/pay/:id", cashier.CreditPayForm)
	cg.POST("/credit/:id/payment", cashier.CreditPay)

	// Admin (manager/admin)
	ag := e.Group("/admin", jwt, middleware.RequireRole(auth.RoleAdmin, auth.RoleManager))
	ag.GET("", admin.Dashboard)
	ag.GET("/products", admin.Products)
	ag.GET("/products/table", admin.ProductsTable)
	ag.GET("/products/form", admin.ProductForm)
	ag.GET("/products/form/:id", admin.ProductForm)
	ag.POST("/products", admin.ProductCreate)
	ag.PUT("/products/:id", admin.ProductUpdate)
	ag.DELETE("/products/:id", admin.ProductDelete)

	ag.GET("/stock", admin.Stock)
	ag.GET("/stock/movements", admin.StockMovements)
	ag.GET("/stock/table", admin.StockTable)
	ag.GET("/stock/form", admin.StockForm)
	ag.POST("/stock/adjust", admin.StockAdjust)
	ag.GET("/stock/damage", admin.DamageForm)
	ag.POST("/stock/damage", admin.DamageRecord)
	ag.GET("/stock/batches/:id", admin.BatchesView)

	ag.GET("/sales", admin.Sales)
	ag.GET("/sales/table", admin.SalesTable)
	ag.GET("/sales/return/:id", admin.SaleReturnForm)
	ag.POST("/sales/:id/return", admin.SaleReturn) // whole-sale return (fallback)

	ag.GET("/customers", admin.Customers)
	ag.GET("/customers/table", admin.CustomersTable)
	ag.GET("/customers/form", admin.CustomerForm)
	ag.GET("/customers/form/:id", admin.CustomerEditForm)
	ag.GET("/customers/pay/:id", admin.CustomerPayForm)
	ag.POST("/customers", admin.CustomerCreate)
	ag.PUT("/customers/:id", admin.CustomerUpdate)
	ag.POST("/customers/:id/payment", admin.CustomerPay)

	// Suppliers
	ag.GET("/suppliers", admin.Suppliers)
	ag.GET("/suppliers/table", admin.SuppliersTable)
	ag.GET("/suppliers/form", admin.SupplierForm)
	ag.GET("/suppliers/form/:id", admin.SupplierForm)
	ag.GET("/suppliers/pay/:id", admin.SupplierPayForm)
	ag.POST("/suppliers", admin.SupplierCreate)
	ag.PUT("/suppliers/:id", admin.SupplierUpdate)
	ag.POST("/suppliers/:id/payment", admin.SupplierPay)
	ag.DELETE("/suppliers/:id", admin.SupplierDelete)

	// Purchases (GRN)
	ag.GET("/purchases", admin.Purchases)
	ag.GET("/purchases/new", admin.PurchaseEntry)
	ag.GET("/purchases/:id", admin.PurchaseDetail)

	// Purchase returns (debit notes)
	ag.GET("/purchase-returns", admin.PurchaseReturns)
	ag.GET("/purchase-returns/new", admin.PurchaseReturnEntry)
	ag.GET("/purchase-returns/:id", admin.PurchaseReturnDetail)

	// Expenses
	ag.GET("/expenses", admin.Expenses)
	ag.GET("/expenses/form", admin.ExpenseForm)
	ag.GET("/expenses/form/:id", admin.ExpenseEditForm)
	ag.POST("/expenses", admin.ExpenseCreate)
	ag.PUT("/expenses/:id", admin.ExpenseUpdate)
	ag.DELETE("/expenses/:id", admin.ExpenseDelete)

	// Finance / profit
	ag.GET("/finance", admin.Finance)

	// Reports
	ag.GET("/reports", admin.ReportsHub)
	ag.GET("/reports/sales", admin.SalesReport)
	ag.GET("/reports/finance", admin.FinanceReport)
	ag.GET("/reports/cash-register", admin.CashRegisterReport)
	ag.GET("/reports/purchases", admin.PurchasesReport)
	ag.GET("/reports/suppliers", admin.SuppliersReport)
	ag.GET("/reports/customer-dues", admin.CustomerDuesReport)
	ag.GET("/reports/supplier-dues", admin.SupplierDuesReport)
	ag.GET("/reports/inventory", admin.InventoryReport)
	ag.GET("/reports/batches", admin.BatchReport)
	ag.GET("/reports/expiring", admin.ExpiringReport)
	ag.GET("/reports/low-stock", admin.LowStockReport)

	// Categories management
	ag.GET("/categories", admin.Categories)
	ag.GET("/categories/table", admin.CategoriesTable)
	ag.GET("/categories/form", admin.CategoryForm)
	ag.GET("/categories/form/:id", admin.CategoryForm)
	ag.POST("/categories", admin.CategoryCreate)
	ag.PUT("/categories/:id", admin.CategoryUpdate)
	ag.DELETE("/categories/:id", admin.CategoryDelete)

	// Units management
	ag.GET("/units", admin.Units)
	ag.GET("/units/table", admin.UnitsTable)
	ag.GET("/units/form", admin.UnitForm)
	ag.GET("/units/form/:id", admin.UnitForm)
	ag.POST("/units", admin.UnitCreate)
	ag.PUT("/units/:id", admin.UnitUpdate)
	ag.DELETE("/units/:id", admin.UnitDelete)

	// Denominations (cash management)
	ag.GET("/denominations", admin.Denominations)
	ag.GET("/denominations/table", admin.DenominationsTable)
	ag.GET("/denominations/form", admin.DenominationForm)
	ag.GET("/denominations/form/:id", admin.DenominationForm)
	ag.POST("/denominations", admin.DenominationCreate)
	ag.PUT("/denominations/:id", admin.DenominationUpdate)
	ag.DELETE("/denominations/:id", admin.DenominationDelete)

	// Cash register sessions (drawer audit + over/short)
	ag.GET("/cash-register", admin.CashSessions)
	ag.GET("/cash-register/:id", admin.CashSessionDetail)
	ag.GET("/cash-register/:id/z", cashier.ZReport) // Z-report (admins may view any session)

	// Barcode labels
	ag.GET("/labels", admin.Labels)
	ag.GET("/labels/print", admin.LabelsPrint)
	ag.POST("/labels/send", admin.LabelsSend)

	// Product conversions
	ag.GET("/conversions", admin.Conversions)
	ag.GET("/conversions/table", admin.ConversionsTable)
	ag.GET("/conversions/form", admin.ConversionForm)
	ag.GET("/conversions/run/:id", admin.ConversionRunForm)
	ag.POST("/conversions", admin.ConversionCreate)
	ag.POST("/conversions/:id/run", admin.ConversionRun)
	ag.DELETE("/conversions/:id", admin.ConversionDelete)

	// Staff users (admin only)
	ag.GET("/users", admin.Users, middleware.RequireRole(auth.RoleAdmin))
	ag.GET("/users/table", admin.UsersTable, middleware.RequireRole(auth.RoleAdmin))
	ag.GET("/users/form", admin.UserForm, middleware.RequireRole(auth.RoleAdmin))
	ag.GET("/users/form/:id", admin.UserEditForm, middleware.RequireRole(auth.RoleAdmin))
	ag.POST("/users", admin.UserCreate, middleware.RequireRole(auth.RoleAdmin))
	ag.PUT("/users/:id", admin.UserUpdate, middleware.RequireRole(auth.RoleAdmin))
	ag.DELETE("/users/:id", admin.UserDeactivate, middleware.RequireRole(auth.RoleAdmin))
	ag.POST("/users/:id/activate", admin.UserReactivate, middleware.RequireRole(auth.RoleAdmin))

	ag.GET("/audit", admin.AuditLog, middleware.RequireRole(auth.RoleAdmin))

	ag.GET("/settings", admin.Settings)
	ag.PUT("/settings", admin.SettingsUpdate)
	ag.POST("/settings/logo", admin.LogoUpload)
	ag.POST("/settings/logo/clear", admin.LogoClear)

	// Backup & restore (admin only — restore replaces all data).
	ag.GET("/backup", admin.Backup, middleware.RequireRole(auth.RoleAdmin))
	ag.POST("/restore", admin.Restore, middleware.RequireRole(auth.RoleAdmin))
}
