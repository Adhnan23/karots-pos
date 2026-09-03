// Package web hosts the server-rendered UI (HTMX + Templ). It imports feature
// services and template packages; feature packages never import templates, so
// there is no import cycle. This is the structural fix for the original plan,
// where ui_handler.go lived inside each feature and imported the templates that
// in turn imported the feature — an illegal cycle in Go.
package web

import (
	"context"
	"net/http"

	"karots-pos/internal/config"
	"karots-pos/internal/features/activity"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/conversions"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/denominations"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/lockers"
	"karots-pos/internal/features/productgroups"
	"karots-pos/internal/features/products"
	adminfragments "karots-pos/templates/fragments/admin"
	"karots-pos/internal/features/purchasereturns"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/recipes"
	"karots-pos/internal/features/recovery"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/supplierpay"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/features/units"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/middleware"
	"karots-pos/internal/plugin"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// Server bundles the services the UI handlers call.
type Server struct {
	cfg              *config.Config
	db               *sqlx.DB
	auth             *auth.Service
	products         *products.Service
	recipes          *recipes.Service
	categories       *categories.Service
	units            *units.Service
	settings         *settings.Service
	sales            *sales.Service
	customers        *customers.Service
	stock            *stock.Service
	suppliers        *suppliers.Service
	supplierPay      *supplierpay.Service
	purchases        *purchases.Service
	purchaseReturns  *purchasereturns.Service
	conversions      *conversions.Service
	expenses         *expenses.Service
	lockers          *lockers.Service
	cashflow         *cashflow.Service
	cashflowReceipts *cashflow.ReceiptService
	reports          *reports.Service
	denominations    *denominations.Service
	cashRegister     *cashregister.Service
	audit            *audit.Service
	activity         *activity.Service
	warranty         *warranty.Service
	recovery         *recovery.Service
	groups           *productgroups.Service
}

// RegisterUI builds UI services and mounts all server-rendered routes. authSvc
// is shared with the API layer so login state is consistent.
func RegisterUI(e *echo.Echo, db *sqlx.DB, cfg *config.Config, authSvc *auth.Service) {
	s := &Server{
		cfg:             cfg,
		db:              db,
		auth:            authSvc,
		products:        products.NewService(db),
		recipes:         recipes.NewService(db),
		categories:      categories.NewService(db),
		units:           units.NewService(db),
		settings:        settings.NewService(db),
		sales:           sales.NewService(db),
		customers:       customers.NewService(db),
		stock:           stock.NewService(db),
		suppliers:       suppliers.NewService(db),
		supplierPay:     supplierpay.NewService(db),
		purchases:       purchases.NewService(db),
		purchaseReturns: purchasereturns.NewService(db),
		conversions:     conversions.NewService(db),
		expenses:        expenses.NewService(db),
		lockers:         lockers.NewService(db),
		reports:         reports.NewService(db),
		denominations:   denominations.NewService(db),
		audit:           audit.NewService(db),
		activity:        activity.NewService(db),
		warranty:        warranty.NewService(db),
		recovery:        recovery.NewService(db),
		groups:          productgroups.NewService(db),
	}
	s.cashRegister = cashregister.NewService(db, sales.NewService(db)).WithAudit(s.audit)
	s.cashflow = cashflow.NewService(db, s.sales)
	s.cashflowReceipts = cashflow.NewReceiptService(db)
	a := &authUI{
		svc:          authSvc,
		cookie:       CookieConfig{Secure: cfg.CookieSecure, MaxAge: cfg.JWTAccessTTL},
		cashRegister: s.cashRegister,
		settings:     s.settings,
		jwtSecret:    cfg.JWTSecret,
	}
	admin := &adminUI{s: s, db: db}
	cashier := &cashierUI{s: s}

	limiter := auth.NewLoginLimiter()
	// Reject tokens whose user has since been deleted or deactivated. One small
	// indexed lookup per authenticated request — negligible for a single-shop POS,
	// and it gives an immediate force-logout. Fails closed (missing user / query
	// error → rejected). The hidden system admin is a real, always-active row, so
	// it is never affected. Installed once here; every JWTAuth instance honours it.
	//
	// The same lookup also carries the per-user supplier flag, so gating that
	// costs no extra query — and revoking it takes effect on the next request
	// rather than whenever the user happens to log in again.
	middleware.SetUserValidator(func(ctx context.Context, userID int64) (middleware.UserFlags, bool) {
		var row struct {
			Active             bool   `db:"is_active"`
			CanHandleSuppliers bool   `db:"can_handle_suppliers"`
			CanManageCredit    bool   `db:"can_manage_credit"`
			CanSeeCost         bool   `db:"can_see_cost"`
			IsSystem           bool   `db:"is_system"`
			Role               string `db:"role"`
		}
		if err := db.GetContext(ctx, &row,
			`SELECT is_active, can_handle_suppliers, can_manage_credit, can_see_cost, is_system, role FROM users WHERE id = $1`, userID); err != nil {
			return middleware.UserFlags{}, false
		}
		return middleware.UserFlags{
			CanHandleSuppliers: row.CanHandleSuppliers,
			CanManageCredit:    row.CanManageCredit,
			CanSeeCost:         row.CanSeeCost,
			// Admin or the hidden support account may escape the kiosk; a cashier
			// never can.
			CanExitKiosk: row.IsSystem || row.Role == "admin",
		}, row.Active
	})
	jwt := middleware.JWTAuth(cfg.JWTSecret)
	// pinGuard forces users carrying a server-assigned PIN to change it before
	// using the app. The /account/pin routes themselves stay ungated.
	pinGuard := middleware.RequirePinChosen()
	// lockGuard bounces a locked session to /lock (screen lock, not a logout).
	// It self-whitelists /lock, /unlock and /logout so the user can always resume
	// or sign out.
	lockGuard := middleware.RequireUnlocked()

	// Public
	e.GET("/login", a.ShowLogin)
	e.POST("/login", a.Login, limiter)
	e.POST("/logout", a.Logout)
	// Also accept GET so a bookmarked/typed /logout (or a plain link) logs out
	// cleanly instead of returning a bare 405. Logout only clears the caller's
	// own cookie and redirects, so it is safe over GET.
	e.GET("/logout", a.Logout)

	// Screen lock (all authenticated roles; NOT lock-gated so they stay reachable
	// while locked). Unlock is rate-limited like login.
	e.GET("/lock", a.ShowLock, jwt)
	e.POST("/lock", a.Lock, jwt)
	e.GET("/lock/config", a.LockConfig, jwt)
	e.POST("/unlock", a.Unlock, jwt, limiter)

	// Kiosk escape: an admin or the support account can drop the till to the
	// desktop (Alt+F4 alone just relaunches). Hidden (404) from cashiers.
	e.POST("/kiosk/exit", a.KioskExit, jwt, middleware.RequireKioskExit())

	// Self-service / forced PIN change (all authenticated roles; NOT pin-gated,
	// so a user forced to change can always reach it; lock-gated so a locked
	// terminal can't change a PIN without unlocking first).
	e.GET("/account/pin", a.ChangePINForm, jwt, lockGuard)
	e.POST("/account/pin", a.ChangePIN, jwt, lockGuard)

	// Root: send the user to their home by role.
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusSeeOther, auth.HomePath(middleware.CurrentRole(c)))
	}, jwt)

	// Cashier (all authenticated roles)
	cg := e.Group("/cashier", jwt, lockGuard, pinGuard)
	cg.GET("", cashier.POS)
	cg.POST("/drawer/open", cashier.OpenDrawer)
	cg.POST("/quick-item", cashier.QuickItem)
	cg.GET("/receipt/:id", cashier.Receipt)
	cg.POST("/print/:id", cashier.PrintReceipt)
	cg.GET("/receipts", cashier.Receipts)
	cg.GET("/receipts/sales", cashier.ReceiptsSales)
	cg.GET("/receipts/cash", cashier.ReceiptsCash)
	cg.GET("/receipts/credit", cashier.ReceiptsCredit)
	cg.GET("/receipts/credit/:id", cashier.DebtReceiptView)
	cg.POST("/receipts/credit/:id/print", cashier.DebtReceiptPrint)
	cg.GET("/receipts/warranty", cashier.ReceiptsWarranty)
	cg.GET("/receipts/warranty/:claimId", cashier.WarrantyReceiptView)
	cg.GET("/money-receipts/:id", cashier.MoneyReceipt)
	cg.POST("/money-receipts/:id/print", cashier.MoneyReceiptPrint)
	cg.POST("/warranty/:claimId/print", cashier.WarrantyReprint)
	cg.GET("/lockers", cashier.CashierLockers) // active lockers for drawer dialogs
	cg.GET("/labels", cashier.Labels)
	cg.POST("/labels/send", cashier.LabelsSend)
	cg.GET("/z/:id", cashier.ZReport) // day-end (Z) report — own session

	// Returns / refunds
	cg.GET("/returns", cashier.Returns)
	cg.GET("/returns/table", cashier.ReturnsTable)
	cg.GET("/returns/:id", cashier.ReturnForm)
	cg.POST("/sales/:id/partial-return", cashier.ReturnSubmit)

	// Damage / write-off
	cg.GET("/conversions", cashier.Conversions)
	cg.GET("/conversions/run/:id", cashier.ConversionRunForm)
	cg.POST("/conversions/:id/run", cashier.ConversionRun)
	cg.GET("/damage", cashier.Damage)
	cg.POST("/damage", cashier.DamageRecord)

	// Credit collection
	cg.GET("/credit", cashier.Credit)
	cg.GET("/credit/table", cashier.CreditTable)
	cg.GET("/credit/pay/:id", cashier.CreditPayForm)
	cg.POST("/credit/:id/payment", cashier.CreditPay)

	// Warranty lookup + replacement (all roles — used at the counter)
	cg.GET("/warranty", cashier.Warranty)
	cg.GET("/warranty/table", cashier.WarrantyTable)
	cg.GET("/warranty/lookup", cashier.WarrantyLookup)
	cg.POST("/warranty/replace", cashier.WarrantyReplace)

	// Suppliers at the counter (per-user permission; admins/managers always pass).
	// A supplier turning up while the cashier is alone used to mean calling the
	// owner away or recording nothing at all.
	sg := cg.Group("/suppliers", middleware.RequireSupplierAccess())
	sg.GET("", cashier.Suppliers)
	sg.GET("/table", cashier.SuppliersTable)
	sg.GET("/pay/:id", cashier.SupplierPayForm)
	sg.POST("/:id/payment", cashier.SupplierPayAtCounter)
	sg.POST("/:id/refund", cashier.SupplierRefundAtCounter)
	sg.GET("/:id/receive", cashier.ReceiveForm)
	sg.POST("/:id/receive", cashier.ReceiveWalkIn)
	sg.GET("/:id/order", cashier.OrderForm)
	sg.POST("/:id/order", cashier.OrderCreate)
	sg.GET("/orders/print", cashier.OrderPrint)
	sg.GET("/orders/:poID/receive", cashier.ReceiveAgainstOrderForm)
	sg.POST("/orders/:poID/receive", cashier.ReceiveAgainstOrder)
	// A supplier or a product met for the first time at the counter. Without
	// these the cashier hits a dead end mid-delivery.
	sg.GET("/new", cashier.SupplierNewForm)
	sg.POST("/new", cashier.SupplierQuickCreate)
	sg.POST("/products", cashier.ProductQuickCreate)
	sg.POST("/products/wanted", cashier.ProductWantedCreate)

	// Expenses at the counter — same counter-operations permission as suppliers.
	// A cashier can book a running cost (bill, bags, repairman) and pay it from
	// their till or a cashier-allowed locker; never the full expense ledger.
	xg := cg.Group("/expenses", middleware.RequireSupplierAccess())
	xg.GET("", cashier.Expenses)
	xg.POST("", cashier.ExpenseRecord)

	// Admin (manager/admin)
	ag := e.Group("/admin", jwt, lockGuard, pinGuard, middleware.RequireRole(auth.RoleAdmin, auth.RoleManager))
	ag.GET("", admin.Dashboard)
	ag.GET("/dashboard/charts", admin.DashboardCharts)
	ag.GET("/products", admin.Products)
	ag.GET("/products/table", admin.ProductsTable)
	ag.GET("/products/review", admin.ProductReview)
	ag.GET("/products/review/table", admin.ProductReviewTable)
	ag.GET("/products/review/count", admin.ProductReviewCount)
	ag.POST("/products/:id/review-done", admin.ProductReviewDone)
	ag.GET("/products/form", admin.ProductForm)
	ag.GET("/products/form/:id", admin.ProductForm)
	ag.GET("/products/:id/barcode", admin.ProductBarcodeForm)
	ag.POST("/products/:id/barcode", admin.ProductBarcodeAssign)

	ag.GET("/groups", admin.Groups)
	ag.GET("/groups/tree", admin.GroupsTree)
	ag.GET("/groups/form", admin.GroupForm)
	ag.GET("/groups/form/:id", admin.GroupForm)
	ag.POST("/groups", admin.GroupCreate)
	ag.PUT("/groups/:id", admin.GroupUpdate)
	ag.DELETE("/groups/:id", admin.GroupDelete)
	ag.POST("/groups/:id/move", admin.GroupMove)
	ag.POST("/groups/:id/activate", admin.GroupReactivate)
	ag.GET("/groups/:id/items", admin.GroupItems)
	ag.POST("/groups/:id/items", admin.GroupItemAdd)
	ag.DELETE("/groups/:id/items/:productId", admin.GroupItemRemove)
	ag.PUT("/groups/:id/items/:productId/emoji", admin.GroupItemEmoji)
	ag.POST("/products", admin.ProductCreate)
	ag.GET("/reports/recipe-variance", admin.RecipeVarianceReport)
	ag.GET("/reports/service-profit", admin.ServiceProfitReport)
	ag.GET("/services", admin.Services)
	ag.GET("/services/table", admin.ServicesTable)
	ag.GET("/services/form", admin.ServiceForm)
	ag.POST("/services", admin.ServiceCreate)
	ag.GET("/products/:id/recipe", admin.ProductRecipeForm)
	ag.POST("/products/:id/recipe", admin.ProductRecipeSave)
	ag.PUT("/products/:id", admin.ProductUpdate)
	ag.DELETE("/products/:id", admin.ProductDelete)
	ag.POST("/products/:id/activate", admin.ProductReactivate)
	ag.GET("/products/import", admin.ProductImportModal)
	ag.GET("/products/import/template", admin.ProductImportTemplate)
	ag.GET("/products/export", admin.ProductExportCSV)
	ag.POST("/products/import", admin.ProductImport)

	ag.GET("/stock", admin.Stock)
	ag.GET("/stock/movements", admin.StockMovements)
	ag.GET("/stock/table", admin.StockTable)
	ag.GET("/stock/levels", admin.StockLevels)
	ag.GET("/stock/form", admin.StockForm)
	ag.POST("/stock/adjust", admin.StockAdjust)
	ag.GET("/inventory/intake", admin.IntakePage)
	ag.POST("/inventory/intake/create", admin.IntakeCreate)
	ag.POST("/inventory/intake/restock", admin.IntakeRestock)
	ag.GET("/stock/take", admin.StockTake)
	ag.POST("/stock/take", admin.StockTakeApply)
	ag.GET("/stock/take/sheet", admin.StockTakeSheet)
	ag.GET("/stock/take/import", admin.StockTakeImportModal)
	ag.POST("/stock/take/import", admin.StockTakeImport)
	ag.GET("/stock/damage", admin.DamageForm)
	ag.POST("/stock/damage", admin.DamageRecord)
	ag.GET("/stock/batches/:id", admin.BatchesView)
	ag.POST("/stock/batches/:id/price", admin.BatchPriceSet)
	ag.GET("/stock/lots/:id", admin.ProductLots)

	ag.GET("/sales", admin.Sales)
	ag.GET("/sales/table", admin.SalesTable)
	ag.GET("/sales/export", admin.SalesExport)
	ag.GET("/sales/return/:id", admin.SaleReturnForm)
	ag.POST("/sales/:id/return", admin.SaleReturn) // whole-sale return (fallback)

	// Section hub landing pages (the sidebar links to these; each lists its
	// subsections as cards). Reports keeps its own richer hub below.
	ag.GET("/sell", admin.sectionHub("sell"))
	ag.GET("/inventory", admin.sectionHub("inventory"))
	ag.GET("/purchasing", admin.sectionHub("purchasing"))
	ag.GET("/money", admin.sectionHub("money"))
	ag.GET("/setup", admin.Setup)

	ag.GET("/customers", admin.Customers)
	ag.GET("/customers/table", admin.CustomersTable)
	ag.GET("/customers/form", admin.CustomerForm)
	ag.GET("/customers/form/:id", admin.CustomerEditForm)
	ag.GET("/customers/opening/:id", admin.CustomerOpeningForm)
	ag.POST("/customers/:id/opening", admin.CustomerOpeningAdjust)
	ag.GET("/customers/pay/:id", admin.CustomerPayForm)
	ag.GET("/customers/:id/statement", admin.CustomerStatement)
	ag.POST("/customers", admin.CustomerCreate)
	ag.PUT("/customers/:id", admin.CustomerUpdate)
	ag.DELETE("/customers/:id", admin.CustomerDelete)
	ag.POST("/customers/:id/activate", admin.CustomerReactivate)
	ag.POST("/customers/:id/payment", admin.CustomerPay)
	ag.GET("/customers/import", admin.CustomerImportModal)
	ag.GET("/customers/import/template", admin.CustomerImportTemplate)
	ag.GET("/customers/export", admin.CustomerExportCSV)
	ag.POST("/customers/import", admin.CustomerImport)

	// Suppliers
	ag.GET("/suppliers", admin.Suppliers)
	ag.GET("/suppliers/table", admin.SuppliersTable)
	ag.GET("/suppliers/form", admin.SupplierForm)
	ag.GET("/suppliers/form/:id", admin.SupplierForm)
	ag.GET("/suppliers/opening/:id", admin.SupplierOpeningForm)
	ag.POST("/suppliers/:id/opening", admin.SupplierOpeningAdjust)
	ag.GET("/suppliers/:id/statement", admin.SupplierStatement)
	ag.GET("/suppliers/pay/:id", admin.SupplierPayForm)
	ag.POST("/suppliers", admin.SupplierCreate)
	ag.PUT("/suppliers/:id", admin.SupplierUpdate)
	ag.POST("/suppliers/:id/payment", admin.SupplierPay)
	ag.POST("/suppliers/:id/refund", admin.SupplierRefund)
	ag.DELETE("/suppliers/:id", admin.SupplierDelete)
	ag.POST("/suppliers/:id/activate", admin.SupplierReactivate)
	ag.GET("/suppliers/reassign/:id", admin.SupplierReassignForm)
	ag.POST("/suppliers/:id/reassign", admin.SupplierReassignApply)
	ag.GET("/suppliers/import", admin.SupplierImportModal)
	ag.GET("/suppliers/import/template", admin.SupplierImportTemplate)
	ag.GET("/suppliers/export", admin.SupplierExportCSV)
	ag.POST("/suppliers/import", admin.SupplierImport)

	// Purchases — drafts (Purchase Orders) → receive
	ag.GET("/purchases", admin.Purchases)
	ag.GET("/purchases/new", admin.PurchaseEntry)
	ag.POST("/purchases", admin.PurchaseEntryCreate)
	ag.POST("/purchases/receive", admin.PurchaseReceiveNow)
	ag.POST("/purchases/draft", admin.PurchaseDraftCreate)
	ag.GET("/purchases/po/print", admin.DraftPOPrint)
	ag.GET("/purchases/:id/grn", admin.GRNPrint)
	ag.GET("/purchases/:id/edit", admin.PurchaseDraftEditForm)
	ag.POST("/purchases/:id/edit", admin.PurchaseDraftUpdate)
	ag.GET("/purchases/:id/receive", admin.PurchaseReceiveForm)
	ag.POST("/purchases/:id/receive", admin.PurchaseReceive)
	ag.POST("/purchases/:id/delete", admin.PurchaseDraftDelete)
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

	// Cash lockers (safe / bank / pocket money locations)
	ag.GET("/lockers", admin.Lockers)
	ag.GET("/lockers/form", admin.LockerForm)
	ag.GET("/lockers/form/:id", admin.LockerEditForm)
	ag.POST("/lockers", admin.LockerCreate)
	ag.PUT("/lockers/:id", admin.LockerUpdate)
	ag.POST("/lockers/:id/archive", admin.LockerArchive)
	ag.GET("/lockers/:id/ledger", admin.LockerLedger)
	ag.GET("/lockers/:id/adjust/form", admin.LockerAdjustForm)
	ag.POST("/lockers/:id/adjust", admin.LockerAdjust)
	ag.GET("/lockers/transfer/form", admin.LockerTransferForm)
	ag.POST("/lockers/transfer", admin.LockerTransfer)
	// Money receipts — one printable, searchable receipt per money move.
	ag.GET("/cashflow", admin.Cashflow)
	ag.GET("/receipts", admin.Receipts)
	ag.GET("/receipts/sales", admin.ReceiptsSales)
	ag.GET("/receipts/cash", admin.ReceiptsCash)
	ag.GET("/receipts/credit", admin.ReceiptsCredit)
	ag.GET("/receipts/credit/:id", admin.DebtReceiptView)
	ag.POST("/receipts/credit/:id/print", admin.DebtReceiptPrint)
	ag.GET("/receipts/warranty", admin.ReceiptsWarranty)
	ag.GET("/receipts/warranty/:claimId", admin.WarrantyReceiptView)
	ag.POST("/receipts/warranty/:claimId/print", admin.WarrantyReprint)
	ag.GET("/money-receipts", admin.MoneyReceipts)
	ag.GET("/money-receipts/:id", admin.MoneyReceipt)
	ag.POST("/money-receipts/:id/print", admin.MoneyReceiptPrint)

	// Warranty (admin shell) + losses & supplier recovery
	ag.GET("/warranty", admin.Warranty)
	ag.GET("/warranty/table", admin.WarrantyTable)
	ag.GET("/warranty/lookup", admin.WarrantyLookup)
	ag.POST("/warranty/replace", admin.WarrantyReplace)
	ag.GET("/damage", admin.DamageReport)
	ag.GET("/damage/table", admin.DamageTable)
	ag.GET("/recovery/form", admin.RecoveryForm)
	ag.POST("/recovery", admin.RecoveryRecord)

	// Finance / profit hub
	ag.GET("/finance", admin.Finance)
	ag.GET("/finance/profit", admin.FinanceProfit)
	ag.GET("/finance/cashflow", admin.FinanceCashflow)

	// Reports
	ag.GET("/reports", admin.ReportsHub)
	ag.GET("/reports/sales", admin.SalesReport)
	ag.GET("/reports/tender", admin.TenderReport)
	ag.GET("/reports/returns", admin.ReturnsReport)
	ag.GET("/reports/profit-by-category", admin.ProfitByCategoryReport)
	ag.GET("/reports/sales-trend", admin.SalesTrendReport)
	ag.GET("/reports/product-sales", admin.ProductSalesReport)
	ag.GET("/reports/top-products", admin.TopProductsReport)
	ag.GET("/reports/warranty", admin.WarrantyReport)
	ag.GET("/reports/finance", admin.FinanceReport)
	ag.GET("/reports/tax", admin.TaxReport)
	ag.GET("/reports/sales-by-cashier", admin.CashierSalesReport)
	ag.GET("/reports/expenses", admin.ExpensesReport)
	ag.GET("/reports/cash-register", admin.CashRegisterReport)
	ag.GET("/reports/purchases", admin.PurchasesReport)
	ag.GET("/reports/customer-dues", admin.CustomerDuesReport)
	ag.GET("/reports/supplier-dues", admin.SupplierDuesReport)
	ag.GET("/reports/inventory", admin.InventoryReport)
	ag.GET("/reports/batches", admin.BatchReport)
	ag.GET("/reports/low-stock", admin.LowStockReport)
	ag.GET("/reports/low-stock/print", admin.LowStockPrint)

	// Categories management
	ag.GET("/categories", admin.Categories)
	ag.GET("/categories/table", admin.CategoriesTable)
	ag.GET("/categories/form", admin.CategoryForm)
	ag.GET("/categories/form/:id", admin.CategoryForm)
	ag.POST("/categories", admin.CategoryCreate)
	ag.POST("/categories/quick", admin.CategoryQuickCreate)
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
	ag.GET("/conversions/runs", admin.ConversionRuns)
	ag.GET("/conversions/:id/edit", admin.ConversionEditForm)
	ag.POST("/conversions", admin.ConversionCreate)
	ag.POST("/conversions/:id/run", admin.ConversionRun)
	ag.PUT("/conversions/:id", admin.ConversionUpdate)
	ag.DELETE("/conversions/:id", admin.ConversionDelete)
	ag.POST("/conversions/:id/activate", admin.ConversionReactivate)

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
	ag.GET("/activity", admin.Activity, middleware.RequireRole(auth.RoleAdmin))
	ag.GET("/activity/table", admin.ActivityTable, middleware.RequireRole(auth.RoleAdmin))
	ag.GET("/activity/export", admin.ActivityExport, middleware.RequireRole(auth.RoleAdmin))

	ag.GET("/settings", admin.Settings)
	ag.PUT("/settings", admin.SettingsUpdate)
	ag.POST("/settings/printer/test", admin.PrinterTest)
	ag.POST("/settings/logo", admin.LogoUpload)
	ag.POST("/settings/logo/clear", admin.LogoClear)

	// Backup & restore (admin only — restore replaces all data).
	ag.GET("/backup", admin.Backup, middleware.RequireRole(auth.RoleAdmin))
	ag.POST("/restore", admin.Restore, middleware.RequireRole(auth.RoleAdmin))

	// Plugins: hand each enabled plugin a registry over the frozen Core API and
	// the scoped route Mux, then mount its routes. Plugin routes mount AFTER all
	// core routes, so an exact-path plugin route overrides the core handler
	// (echo last-write-wins for exact matches). Plugin cashier/admin routes
	// inherit the same jwt/pinGuard/RequireRole middleware as their core peers.
	// With no plugins compiled in this is a no-op.
	core := plugin.Core{
		DB: db, Cfg: cfg,
		Audit: s.audit, Settings: s.settings, CashRegister: s.cashRegister,
		Sales: s.sales, Expenses: s.expenses, Products: s.products,
		Stock: s.stock,
	}
	reg := plugin.NewRegistry(core, plugin.NewMux(), e)
	plugin.SetupAll(reg)
	// Bridge every plugin's product-search contributor into the single products
	// search seam. Fanning out over the registered slice means two plugins (e.g.
	// custom fields + alternatives) both contribute, instead of the last one to set
	// the func var silently clobbering the others. A contributor error is skipped,
	// never failing the search.
	if scs := plugin.ProductSearchContributors(); len(scs) > 0 {
		products.SearchContributor = func(ctx context.Context, q string) ([]int64, error) {
			var all []int64
			for _, sc := range scs {
				if sc.Match == nil {
					continue
				}
				ids, err := sc.Match(ctx, q)
				if err != nil {
					continue
				}
				all = append(all, ids...)
			}
			return all, nil
		}
	}
	// Same bridge for till-card badge providers → products.BadgeProvider.
	if bps := plugin.ProductBadgeProviders(); len(bps) > 0 {
		products.BadgeProvider = func(ctx context.Context, ids []int64) map[int64][]string {
			out := map[int64][]string{}
			for _, bp := range bps {
				if bp.Batch == nil {
					continue
				}
				m, err := bp.Batch(ctx, ids)
				if err != nil {
					continue
				}
				for id, badges := range m {
					out[id] = append(out[id], badges...)
				}
			}
			return out
		}
	}
	// Same bridge for till sale-suggestion providers → products.SaleSuggestionProvider.
	if sps := plugin.ProductSaleSuggestionProviders(); len(sps) > 0 {
		products.SaleSuggestionProvider = func(ctx context.Context, ids []int64) map[int64]products.SaleSuggestion {
			out := map[int64]products.SaleSuggestion{}
			for _, sp := range sps {
				if sp.Batch == nil {
					continue
				}
				m, err := sp.Batch(ctx, ids)
				if err != nil {
					continue
				}
				for id, s := range m {
					if _, taken := out[id]; taken {
						continue // first provider wins; in practice only one registers
					}
					out[id] = products.SaleSuggestion{
						DiscountType: s.DiscountType, DiscountValue: s.DiscountValue,
						Label: s.Label, Prompt: s.Prompt,
					}
				}
			}
			return out
		}
	}
	// Same bridge for till info-popup detail contributors → products.DetailContributor.
	if dcs := plugin.ProductDetailContributors(); len(dcs) > 0 {
		products.DetailContributor = func(ctx context.Context, id int64) []products.DetailRow {
			var out []products.DetailRow
			for _, dc := range dcs {
				if dc.Rows == nil {
					continue
				}
				rows, err := dc.Rows(ctx, id)
				if err != nil {
					continue
				}
				for _, r := range rows {
					out = append(out, products.DetailRow{Label: r.Label, Value: r.Value})
				}
			}
			return out
		}
	}
	// Same bridge for barcode-label field providers → products.LabelFieldProvider.
	if lps := plugin.ProductLabelFieldProviders(); len(lps) > 0 {
		products.LabelFieldProvider = func(ctx context.Context, id int64) []products.DetailRow {
			var out []products.DetailRow
			for _, lp := range lps {
				if lp.Rows == nil {
					continue
				}
				rows, err := lp.Rows(ctx, id)
				if err != nil {
					continue
				}
				for _, r := range rows {
					out = append(out, products.DetailRow{Label: r.Label, Value: r.Value})
				}
			}
			return out
		}
	}
	// Plugin-contributed product-list row actions (e.g. Product Plus "Duplicate")
	// → adminfragments package var, so the templates package never imports plugin.
	for _, a := range plugin.ProductRowActions() {
		adminfragments.ProductRowActions = append(adminfragments.ProductRowActions, adminfragments.RowAction{
			Label: a.Label,
			Href:  a.Href,
		})
	}
	reg.Mux.Mount(e.Group(""), cg, ag)
}
