// Package reports computes the shop's financials: a profit & loss summary plus
// supporting breakdowns over a date range.
package reports

import (
	"context"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

// PL is a profit & loss summary for [From, To).
type PL struct {
	From         time.Time       `json:"from"`
	To           time.Time       `json:"to"`
	SalesCount   int             `json:"sales_count"`
	GrossRevenue decimal.Decimal `json:"gross_revenue"` // sale totals before returns (excl. void)
	Returns      decimal.Decimal `json:"returns"`       // value of returned lines, by sale date
	Revenue      decimal.Decimal `json:"revenue"`       // net revenue = gross - returns
	Received     decimal.Decimal `json:"received"`      // tender taken AT point of sale (excludes later debt collections — see Cashflow view for true cash in)
	COGS         decimal.Decimal `json:"cogs"`          // cost of goods sold, net of returns
	GrossProfit  decimal.Decimal `json:"gross_profit"`  // revenue - COGS
	GrossMargin  decimal.Decimal `json:"gross_margin"`  // gross profit / revenue, percent
	Expenses     decimal.Decimal `json:"expenses"`      // operating expenses in range
	Losses       decimal.Decimal `json:"losses"`        // damage + warranty replacement cost
	OwnUse       decimal.Decimal `json:"own_use"`       // stock the shop consumed itself
	StaffWelfare decimal.Decimal `json:"staff_welfare"` // stock taken by staff
	Recoveries   decimal.Decimal `json:"recoveries"`    // value reclaimed from suppliers
	OtherIncome  decimal.Decimal `json:"other_income"`  // bank interest credited to lockers in range
	// StockCorrections is the net value of stock adjustments: positive when a
	// count wrote stock OFF, negative when it found more than the books showed.
	// Shops that correct stock this way (rather than by stock-take) book all of
	// their shrinkage here, so it has to reach the profit line.
	StockCorrections decimal.Decimal `json:"stock_corrections"`
	// ZeroCostRevenue is revenue from stocked lines that were sold at no recorded
	// cost — a product captured without a cost price, whose opening lot is
	// therefore worth nothing. Those lines read as pure profit, so the gross
	// margin above is overstated by however much they really cost. Reported so
	// the number carries its own caveat instead of quietly flattering the shop.
	ZeroCostRevenue decimal.Decimal `json:"zero_cost_revenue"`
	ZeroCostLines   int             `json:"zero_cost_lines"`
	NetProfit       decimal.Decimal `json:"net_profit"`  // gross - expenses - losses - own use - staff welfare - stock corrections + recoveries + other income
	Receivables     decimal.Decimal `json:"receivables"` // customer dues (current snapshot)
	Payables        decimal.Decimal `json:"payables"`    // supplier dues (current snapshot; credits excluded)
	// SupplierCredit is the mirror of Payables: what suppliers owe US, from
	// returned goods or advances paid ahead of a delivery.
	SupplierCredit decimal.Decimal  `json:"supplier_credit"`
	CashWithdrawn  decimal.Decimal  `json:"cash_withdrawn"` // drawer withdrawals in range
	RegisterDiff   decimal.Decimal  `json:"register_diff"`  // net over/short of sessions closed in range
	TopProducts    []ProductRevenue `json:"top_products"`
	ByPayment      []MethodTotal    `json:"by_payment"`
}

type ProductRevenue struct {
	ProductName string          `db:"product_name" json:"product_name"`
	Qty         decimal.Decimal `db:"qty"          json:"qty"`
	Revenue     decimal.Decimal `db:"revenue"      json:"revenue"`
	Profit      decimal.Decimal `db:"profit"       json:"profit"`
}

type MethodTotal struct {
	Method string          `db:"method" json:"method"`
	Total  decimal.Decimal `db:"total"  json:"total"`
}

type Service struct{ db *sqlx.DB }

func NewService(db *sqlx.DB) *Service { return &Service{db: db} }

func (s *Service) Compute(ctx context.Context, from, to time.Time) (*PL, error) {
	pl := &PL{From: from, To: to}

	// Gross sales (every real sale at its original total) and cash collected. A
	// return is accounted by sale date: it reduces this sale's period below via the
	// Returns figure and the net COGS, rather than dropping the whole sale.
	var head struct {
		Count int             `db:"count"`
		Gross decimal.Decimal `db:"gross"`
		Paid  decimal.Decimal `db:"paid"`
	}
	if err := s.db.GetContext(ctx, &head, `
		SELECT COUNT(*) AS count,
		       COALESCE(SUM(total),0) AS gross,
		       COALESCE(SUM(paid_amount),0) AS paid
		FROM sales
		WHERE status <> 'void' AND created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute revenue", err)
	}
	pl.SalesCount = head.Count
	pl.GrossRevenue = head.Gross
	pl.Received = head.Paid

	// Returns: value of returned lines (valued per unit as line net / qty, matching
	// the refund actually given), for sales in this period.
	if err := s.db.GetContext(ctx, &pl.Returns, `
		SELECT COALESCE(SUM( (si.subtotal / NULLIF(si.quantity,0)) * si.returned_qty ),0)
		FROM sale_items si JOIN sales s ON s.id = si.sale_id
		WHERE s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute returns", err)
	}
	pl.Revenue = pl.GrossRevenue.Sub(pl.Returns)

	// COGS net of returns — returned units are restocked, so their cost is excluded.
	if err := s.db.GetContext(ctx, &pl.COGS, `
		SELECT COALESCE(SUM( (si.quantity - si.returned_qty) * si.cost_price ),0)
		FROM sale_items si JOIN sales s ON s.id = si.sale_id
		WHERE s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute COGS", err)
	}
	pl.GrossProfit = pl.Revenue.Sub(pl.COGS)
	pl.GrossMargin = grossMargin(pl.GrossProfit, pl.Revenue)

	if err := s.db.GetContext(ctx, &pl.Expenses, `
		SELECT COALESCE(SUM(amount),0) FROM expenses
		WHERE expense_date >= $1 AND expense_date < $2`, from, to); err != nil {
		return nil, apperr.Internal("failed to compute expenses", err)
	}
	// Stock losses (damaged + warranty replacements) and any value reclaimed from
	// suppliers against them. These never touch revenue, so without these lines the
	// P&L would silently overstate profit by the cost of the lost goods.
	if err := s.db.GetContext(ctx, &pl.Losses, `
		SELECT COALESCE(SUM(cost),0) FROM stock_movements
		WHERE type IN ('damage','warranty_replacement') AND created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute losses", err)
	}
	// Stock the shop consumed itself, and stock taken by staff. Kept off the
	// Losses line on purpose: both are deliberate and expected, and folding them
	// into losses would make breakage look worse than it is while hiding how
	// much the shop actually consumes.
	if err := s.db.GetContext(ctx, &pl.OwnUse, `
		SELECT COALESCE(SUM(cost),0) FROM stock_movements
		WHERE type = 'own_use' AND created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute own-use cost", err)
	}
	if err := s.db.GetContext(ctx, &pl.StaffWelfare, `
		SELECT COALESCE(SUM(cost),0) FROM stock_movements
		WHERE type = 'staff' AND created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute staff welfare cost", err)
	}
	if err := s.db.GetContext(ctx, &pl.Recoveries, `
		SELECT COALESCE(SUM(recovered_value),0) FROM loss_recoveries
		WHERE created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute recoveries", err)
	}

	// Other income: bank interest credited to a locker in the period (recorded via
	// the locker "Adjust → Interest" path as a locker_ledger row). Bank charges
	// already hit the P&L through expenses; interest is the symmetric income side,
	// read straight from the ledger (no separate income subsystem).
	_ = s.db.GetContext(ctx, &pl.OtherIncome, `
		SELECT COALESCE(SUM(balance_delta),0) FROM locker_ledger
		WHERE kind = 'interest' AND created_at >= $1 AND created_at < $2`, from, to)

	// Stock corrections: what counting the shelves added or took away, valued at
	// the cost of the lots involved. Kept off Losses because a correction is not
	// breakage — it is the count disagreeing with the books, and it swings both
	// ways. Positive here means value written OFF (quantity carries the sign, the
	// cost column is always the positive worth moved).
	_ = s.db.GetContext(ctx, &pl.StockCorrections, `
		SELECT COALESCE(SUM(CASE WHEN quantity < 0 THEN cost ELSE -cost END),0)
		FROM stock_movements
		WHERE type = 'adjust' AND created_at >= $1 AND created_at < $2`, from, to)

	// Stocked lines that cost nothing on the books. Services are excluded: they
	// legitimately have no stock cost.
	_ = s.db.GetContext(ctx, &pl.ZeroCostRevenue, `
		SELECT COALESCE(SUM(si.subtotal),0)
		FROM sale_items si
		JOIN sales s ON s.id = si.sale_id
		JOIN products p ON p.id = si.product_id
		WHERE NOT p.is_service AND COALESCE(si.cost_price,0) = 0
		  AND s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2`, from, to)
	_ = s.db.GetContext(ctx, &pl.ZeroCostLines, `
		SELECT COUNT(*)
		FROM sale_items si
		JOIN sales s ON s.id = si.sale_id
		JOIN products p ON p.id = si.product_id
		WHERE NOT p.is_service AND COALESCE(si.cost_price,0) = 0
		  AND s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2`, from, to)

	_ = s.db.GetContext(ctx, &pl.RegisterDiff, `
		SELECT COALESCE(SUM(difference),0) FROM cash_register
		WHERE closed_at >= $1 AND closed_at < $2`, from, to)

	// The till drawer coming up short is money gone, the same as any other loss —
	// standard retail books it as "cash over/short". RegisterDiff is negative when
	// short, so adding it subtracts the shortage and credits an overage.
	pl.NetProfit = pl.GrossProfit.Sub(pl.Expenses).Sub(pl.Losses).
		Sub(pl.OwnUse).Sub(pl.StaffWelfare).Sub(pl.StockCorrections).
		Add(pl.Recoveries).Add(pl.OtherIncome).Add(pl.RegisterDiff)

	_ = s.db.GetContext(ctx, &pl.Receivables, `SELECT COALESCE(SUM(outstanding_balance),0) FROM customers`)
	// Only suppliers we actually OWE count as payables. A supplier in credit (goods
	// returned, or an advance paid) has a negative balance, and summing it raw
	// netted that credit against everyone else's invoices — understating the debt.
	// The credit is a receivable, reported separately.
	_ = s.db.GetContext(ctx, &pl.Payables,
		`SELECT COALESCE(SUM(GREATEST(outstanding_balance,0)),0) FROM suppliers`)
	_ = s.db.GetContext(ctx, &pl.SupplierCredit,
		`SELECT COALESCE(SUM(GREATEST(-outstanding_balance,0)),0) FROM suppliers`)

	// Cash drawer: total withdrawn and the net over/short of sessions closed in
	// the period (negative = short).
	_ = s.db.GetContext(ctx, &pl.CashWithdrawn, `
		SELECT COALESCE(SUM(ABS(amount)),0) FROM cash_movements
		WHERE type = 'withdrawal' AND created_at >= $1 AND created_at < $2`, from, to)

	// Top products on net (kept) quantities and value.
	if err := s.db.SelectContext(ctx, &pl.TopProducts, `
		SELECT p.name AS product_name,
		       SUM(si.quantity - si.returned_qty) AS qty,
		       SUM( (si.subtotal / NULLIF(si.quantity,0)) * (si.quantity - si.returned_qty) ) AS revenue,
		       SUM( (si.subtotal / NULLIF(si.quantity,0)) * (si.quantity - si.returned_qty)
		            - (si.quantity - si.returned_qty) * si.cost_price ) AS profit
		FROM sale_items si
		JOIN sales s ON s.id = si.sale_id
		JOIN products p ON p.id = si.product_id
		WHERE s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2
		GROUP BY p.name
		HAVING SUM(si.quantity - si.returned_qty) > 0
		ORDER BY revenue DESC
		LIMIT 10`, from, to); err != nil {
		return nil, apperr.Internal("failed to compute top products", err)
	}

	if err := s.db.SelectContext(ctx, &pl.ByPayment, `
		SELECT pmt.method, COALESCE(SUM(pmt.amount),0) AS total
		FROM payments pmt JOIN sales s ON s.id = pmt.sale_id
		WHERE s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2
		GROUP BY pmt.method ORDER BY total DESC`, from, to); err != nil {
		return nil, apperr.Internal("failed to compute payment breakdown", err)
	}

	return pl, nil
}

// grossMargin is gross profit as a percent of net revenue (0 when revenue is 0).
func grossMargin(profit, revenue decimal.Decimal) decimal.Decimal {
	if !revenue.IsPositive() {
		return decimal.Zero
	}
	return profit.Div(revenue).Mul(decimal.NewFromInt(100)).Round(2)
}

// ReturnRow is one returned line for the returns report.
type ReturnRow struct {
	SaleDate    time.Time       `db:"sale_date"    json:"sale_date"`
	ReceiptNo   string          `db:"receipt_no"   json:"receipt_no"`
	ProductName string          `db:"product_name" json:"product_name"`
	Qty         decimal.Decimal `db:"qty"          json:"qty"`
	RefundValue decimal.Decimal `db:"refund_value" json:"refund_value"`
	Customer    *string         `db:"customer"     json:"customer,omitempty"`
}

// Returns lists returned lines (full or partial) for sales in the period.
func (s *Service) Returns(ctx context.Context, from, to time.Time) ([]ReturnRow, error) {
	var rows []ReturnRow
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT s.created_at AS sale_date, s.receipt_no, p.name AS product_name,
		       si.returned_qty AS qty,
		       ((si.subtotal / NULLIF(si.quantity,0)) * si.returned_qty) AS refund_value,
		       c.name AS customer
		FROM sale_items si
		JOIN sales s     ON s.id = si.sale_id
		JOIN products p  ON p.id = si.product_id
		LEFT JOIN customers c ON c.id = s.customer_id
		WHERE si.returned_qty > 0 AND s.status <> 'void'
		  AND s.created_at >= $1 AND s.created_at < $2
		ORDER BY s.created_at DESC, s.receipt_no`, from, to); err != nil {
		return nil, apperr.Internal("failed to load returns", err)
	}
	return rows, nil
}

// CategoryProfit is net revenue/COGS/profit for one category in the period.
type CategoryProfit struct {
	Category string          `db:"category" json:"category"`
	Revenue  decimal.Decimal `db:"revenue"  json:"revenue"`
	COGS     decimal.Decimal `db:"cogs"     json:"cogs"`
	Profit   decimal.Decimal `db:"profit"   json:"profit"`
}

// ProfitByCategory groups net sales by product category for the period. When
// `cats` is non-empty, only those category names are included.
func (s *Service) ProfitByCategory(ctx context.Context, from, to time.Time, cats ...string) ([]CategoryProfit, error) {
	var rows []CategoryProfit
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT cat.name AS category,
		       SUM( (si.subtotal / NULLIF(si.quantity,0)) * (si.quantity - si.returned_qty) ) AS revenue,
		       SUM( (si.quantity - si.returned_qty) * si.cost_price ) AS cogs,
		       SUM( (si.subtotal / NULLIF(si.quantity,0)) * (si.quantity - si.returned_qty)
		            - (si.quantity - si.returned_qty) * si.cost_price ) AS profit
		FROM sale_items si
		JOIN sales s     ON s.id = si.sale_id
		JOIN products p  ON p.id = si.product_id
		JOIN categories cat ON cat.id = p.category_id
		WHERE s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2
		  AND ($3::text[] IS NULL OR cardinality($3::text[]) = 0 OR cat.name = ANY($3::text[]))
		GROUP BY cat.name
		HAVING SUM(si.quantity - si.returned_qty) > 0
		ORDER BY profit DESC`, from, to, pq.Array(cats)); err != nil {
		return nil, apperr.Internal("failed to load category profit", err)
	}
	return rows, nil
}

// CategoryNames lists all category names (for the report's category filter).
func (s *Service) CategoryNames(ctx context.Context) ([]string, error) {
	var names []string
	if err := s.db.SelectContext(ctx, &names, `SELECT name FROM categories ORDER BY name`); err != nil {
		return nil, apperr.Internal("failed to load categories", err)
	}
	return names, nil
}

// DayRow is one day's net sales for the trend report.
type DayRow struct {
	Day     time.Time       `db:"day"     json:"day"`
	Count   int             `db:"count"   json:"count"`
	Revenue decimal.Decimal `db:"revenue" json:"revenue"`
	Profit  decimal.Decimal `db:"profit"  json:"profit"`
}

// DailySales is per-day net revenue and profit for the period.
func (s *Service) DailySales(ctx context.Context, from, to time.Time) ([]DayRow, error) {
	return s.SalesByPeriod(ctx, from, to, "day")
}

// SalesByPeriod is net revenue/profit grouped by day, week or month. `gran` must
// be one of day/week/month; anything else falls back to day. Each row's Day is
// the period start (truncated date).
func (s *Service) SalesByPeriod(ctx context.Context, from, to time.Time, gran string) ([]DayRow, error) {
	switch gran {
	case "day", "week", "month":
	default:
		gran = "day"
	}
	var rows []DayRow
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT d.day, d.count, d.revenue, d.revenue - d.cogs AS profit
		FROM (
			SELECT date_trunc($3, s.created_at) AS day,
			       COUNT(DISTINCT s.id) AS count,
			       COALESCE(SUM( (si.subtotal / NULLIF(si.quantity,0)) * (si.quantity - si.returned_qty) ),0) AS revenue,
			       COALESCE(SUM( (si.quantity - si.returned_qty) * si.cost_price ),0) AS cogs
			FROM sales s
			JOIN sale_items si ON si.sale_id = s.id
			WHERE s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2
			GROUP BY 1
		) d
		ORDER BY d.day`, from, to, gran); err != nil {
		return nil, apperr.Internal("failed to load sales by period", err)
	}
	return rows, nil
}

// ProductPeriodRow is one product's net qty + revenue for a single period.
type ProductPeriodRow struct {
	Day     time.Time       `db:"day"     json:"day"`
	Qty     decimal.Decimal `db:"qty"     json:"qty"`
	Revenue decimal.Decimal `db:"revenue" json:"revenue"`
}

// ProductSalesByPeriod is one product's net units + revenue grouped by day/week/
// month over [from,to). Powers the per-product sales graph (with a last-year
// overlay obtained by calling it again over the shifted range).
func (s *Service) ProductSalesByPeriod(ctx context.Context, productID int64, from, to time.Time, gran string) ([]ProductPeriodRow, error) {
	switch gran {
	case "day", "week", "month":
	default:
		gran = "day"
	}
	var rows []ProductPeriodRow
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT date_trunc($4, s.created_at) AS day,
		       COALESCE(SUM(si.quantity - si.returned_qty),0) AS qty,
		       COALESCE(SUM( (si.subtotal / NULLIF(si.quantity,0)) * (si.quantity - si.returned_qty) ),0) AS revenue
		FROM sales s
		JOIN sale_items si ON si.sale_id = s.id
		WHERE s.status <> 'void' AND si.product_id = $1
		  AND s.created_at >= $2 AND s.created_at < $3
		GROUP BY 1 ORDER BY 1`, productID, from, to, gran); err != nil {
		return nil, apperr.Internal("failed to load product sales", err)
	}
	return rows, nil
}

// ProductQtySold returns net units sold per product over [from,to) for the given
// product ids — the demand input for reorder forecasting.
func (s *Service) ProductQtySold(ctx context.Context, ids []int64, from, to time.Time) (map[int64]decimal.Decimal, error) {
	out := make(map[int64]decimal.Decimal, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ProductID int64           `db:"product_id"`
		Qty       decimal.Decimal `db:"qty"`
	}
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT si.product_id, COALESCE(SUM(si.quantity - si.returned_qty),0) AS qty
		FROM sale_items si
		JOIN sales s ON s.id = si.sale_id
		WHERE s.status <> 'void' AND si.product_id = ANY($1)
		  AND s.created_at >= $2 AND s.created_at < $3
		GROUP BY si.product_id`, pq.Array(ids), from, to); err != nil {
		return nil, apperr.Internal("failed to load product demand", err)
	}
	for _, r := range rows {
		out[r.ProductID] = r.Qty
	}
	return out, nil
}

// WarrantySummary is the periodised warranty-cost vs supplier-recovery picture.
type WarrantySummary struct {
	ReplacementCount int             `json:"replacement_count"`
	ReplacementCost  decimal.Decimal `json:"replacement_cost"`
	DamageCount      int             `json:"damage_count"`
	DamageCost       decimal.Decimal `json:"damage_cost"`
	Recoveries       []RecoveryRow   `json:"recoveries"`
	RecoveredValue   decimal.Decimal `json:"recovered_value"`
}

// RecoveryRow is recovery totals for one outcome.
type RecoveryRow struct {
	Outcome   string          `db:"outcome"   json:"outcome"`
	Count     int             `db:"count"     json:"count"`
	LossValue decimal.Decimal `db:"loss_value" json:"loss_value"`
	Recovered decimal.Decimal `db:"recovered" json:"recovered"`
}

// WarrantyAndRecovery summarises stock losses and supplier recoveries by period.
func (s *Service) WarrantyAndRecovery(ctx context.Context, from, to time.Time) (*WarrantySummary, error) {
	out := &WarrantySummary{}
	var moves []struct {
		Type  string          `db:"type"`
		Count int             `db:"count"`
		Cost  decimal.Decimal `db:"cost"`
	}
	if err := s.db.SelectContext(ctx, &moves, `
		SELECT type, COUNT(*) AS count, COALESCE(SUM(cost),0) AS cost
		FROM stock_movements
		WHERE type IN ('warranty_replacement','damage') AND created_at >= $1 AND created_at < $2
		GROUP BY type`, from, to); err != nil {
		return nil, apperr.Internal("failed to load losses", err)
	}
	for _, m := range moves {
		switch m.Type {
		case "warranty_replacement":
			out.ReplacementCount, out.ReplacementCost = m.Count, m.Cost
		case "damage":
			out.DamageCount, out.DamageCost = m.Count, m.Cost
		}
	}
	if err := s.db.SelectContext(ctx, &out.Recoveries, `
		SELECT outcome, COUNT(*) AS count,
		       COALESCE(SUM(loss_value),0) AS loss_value,
		       COALESCE(SUM(recovered_value),0) AS recovered
		FROM loss_recoveries
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY outcome ORDER BY outcome`, from, to); err != nil {
		return nil, apperr.Internal("failed to load recoveries", err)
	}
	for _, r := range out.Recoveries {
		out.RecoveredValue = out.RecoveredValue.Add(r.Recovered)
	}
	return out, nil
}

// TaxDayRow is one day's taxable base (net sales excl. tax) and tax charged.
type TaxDayRow struct {
	Day   time.Time       `db:"day"   json:"day"`
	Count int             `db:"count" json:"count"`
	Base  decimal.Decimal `db:"base"  json:"base"`
	Tax   decimal.Decimal `db:"tax"   json:"tax"`
}

// TaxSummary is the per-day and total tax charged on sales for a period.
type TaxSummary struct {
	Rows      []TaxDayRow     `json:"rows"`
	Count     int             `json:"count"`
	TotalBase decimal.Decimal `json:"total_base"`
	TotalTax  decimal.Decimal `json:"total_tax"`
}

// TaxSummary aggregates tax charged on (non-void) sales per day. Base is the
// pre-tax amount (total − tax); this reflects tax charged at sale time and is
// not net of subsequent returns.
func (s *Service) TaxSummary(ctx context.Context, from, to time.Time) (*TaxSummary, error) {
	var rows []TaxDayRow
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT date_trunc('day', created_at) AS day,
		       COUNT(*) AS count,
		       COALESCE(SUM(total - tax),0) AS base,
		       COALESCE(SUM(tax),0) AS tax
		FROM sales
		WHERE status <> 'void' AND created_at >= $1 AND created_at < $2
		GROUP BY 1 ORDER BY 1`, from, to); err != nil {
		return nil, apperr.Internal("failed to load tax summary", err)
	}
	out := &TaxSummary{Rows: rows}
	for _, r := range rows {
		out.TotalBase = out.TotalBase.Add(r.Base)
		out.TotalTax = out.TotalTax.Add(r.Tax)
		out.Count += r.Count
	}
	return out, nil
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) Finance(c echo.Context) error {
	from, to, err := ParseRange(c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return err
	}
	pl, err := h.svc.Compute(c.Request().Context(), from, to)
	if err != nil {
		return err
	}
	return response.OK(c, pl)
}

// ParseRange parses optional from/to (YYYY-MM-DD), defaulting to the current
// calendar month. `to` is made exclusive (end of day).
func ParseRange(fromStr, toStr string) (time.Time, time.Time, error) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	to := from.AddDate(0, 1, 0)
	if fromStr != "" {
		t, err := time.Parse("2006-01-02", fromStr)
		if err != nil {
			return from, to, apperr.BadRequest("from must be YYYY-MM-DD")
		}
		from = t
	}
	if toStr != "" {
		t, err := time.Parse("2006-01-02", toStr)
		if err != nil {
			return from, to, apperr.BadRequest("to must be YYYY-MM-DD")
		}
		to = t.AddDate(0, 0, 1)
	}
	return from, to, nil
}

// ResolveRange turns an optional quick-pick preset (today, this-week, this-month,
// last-week, last-month, this-year) into a [from, to) range plus the inclusive
// YYYY-MM-DD display strings for the form. A non-empty preset wins; otherwise it
// falls back to ParseRange(fromStr, toStr). Weeks run Monday–Sunday. The returned
// `to` is exclusive (start of the day after the last day in range).
func ResolveRange(preset, fromStr, toStr string) (from, to time.Time, fromOut, toOut string, err error) {
	if preset == "" {
		from, to, err = ParseRange(fromStr, toStr)
		if err != nil {
			return
		}
		fromOut = fromStr
		if fromOut == "" {
			fromOut = from.Format("2006-01-02")
		}
		toOut = toStr
		if toOut == "" {
			toOut = to.AddDate(0, 0, -1).Format("2006-01-02")
		}
		return
	}

	now := time.Now()
	loc := now.Location()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch preset {
	case "today":
		from, to = day, day.AddDate(0, 0, 1)
	case "this-week":
		from = weekStart(day)
		to = from.AddDate(0, 0, 7)
	case "last-week":
		to = weekStart(day)
		from = to.AddDate(0, 0, -7)
	case "this-month":
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		to = from.AddDate(0, 1, 0)
	case "last-month":
		to = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		from = to.AddDate(0, -1, 0)
	case "this-year":
		from = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc)
		to = from.AddDate(1, 0, 0)
	default:
		err = apperr.BadRequest("unknown preset")
		return
	}
	fromOut = from.Format("2006-01-02")
	toOut = to.AddDate(0, 0, -1).Format("2006-01-02")
	return
}

// weekStart returns the Monday 00:00 of the week containing d.
func weekStart(d time.Time) time.Time {
	off := (int(d.Weekday()) + 6) % 7 // Mon=0 … Sun=6
	return d.AddDate(0, 0, -off)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/reports", middleware.JWTAuth(cfg.JWTSecret), middleware.RequireRole("admin", "manager"))
	g.GET("/finance", api.Finance)
}

// ServiceProfitRow is one service's whole economic picture for a period:
// what it earned, what its recipe consumed, and what running it cost.
type ServiceProfitRow struct {
	ProductID int64           `db:"product_id"`
	Name      string          `db:"name"`
	Units     decimal.Decimal `db:"units"`
	Revenue   decimal.Decimal `db:"revenue"`
	COGS      decimal.Decimal `db:"cogs"`
	Expenses  decimal.Decimal `db:"expenses"`
}

// GrossProfit is revenue less what the recipe consumed.
func (r ServiceProfitRow) GrossProfit() decimal.Decimal { return r.Revenue.Sub(r.COGS) }

// NetProfit also subtracts expenses attributed to this service — the toner
// change, the machine repair. Those are not per-unit costs, so they never enter
// COGS; they are subtracted once, here, where the question is "did this counter
// pay for itself".
func (r ServiceProfitRow) NetProfit() decimal.Decimal { return r.GrossProfit().Sub(r.Expenses) }

// MarginPct is net profit as a percentage of revenue; zero when nothing sold.
func (r ServiceProfitRow) MarginPct() decimal.Decimal {
	if r.Revenue.IsZero() {
		return decimal.Zero
	}
	return r.NetProfit().Div(r.Revenue).Mul(decimal.NewFromInt(100)).Round(1)
}

// ServiceProfit reports each service product's income, ingredient cost and
// attributed running costs for a period.
//
// Services are absent from every stock-facing report (they hold no inventory),
// and the shop-wide P&L blends them into one number, so there was no way to ask
// "what did the coffee machine actually make me". A service with no sales but
// real expenses still appears: a counter that cost money and earned nothing is
// exactly what this report has to be able to show.
func (s *Service) ServiceProfit(ctx context.Context, from, to time.Time) ([]ServiceProfitRow, error) {
	var rows []ServiceProfitRow
	if err := s.db.SelectContext(ctx, &rows, `
		WITH sold AS (
			SELECT si.product_id,
			       SUM(si.quantity - si.returned_qty) AS units,
			       SUM( (si.subtotal / NULLIF(si.quantity,0)) * (si.quantity - si.returned_qty) ) AS revenue,
			       SUM( (si.quantity - si.returned_qty) * si.cost_price ) AS cogs
			FROM sale_items si
			JOIN sales sa ON sa.id = si.sale_id
			WHERE sa.status <> 'void' AND sa.created_at >= $1 AND sa.created_at < $2
			GROUP BY si.product_id
		),
		tagged AS (
			SELECT service_product_id AS product_id, SUM(amount) AS expenses
			FROM expenses
			WHERE service_product_id IS NOT NULL
			  AND expense_date >= $1 AND expense_date < $2
			GROUP BY service_product_id
		)
		SELECT p.id AS product_id, p.name,
		       COALESCE(so.units,0)    AS units,
		       COALESCE(so.revenue,0)  AS revenue,
		       COALESCE(so.cogs,0)     AS cogs,
		       COALESCE(tg.expenses,0) AS expenses
		FROM products p
		LEFT JOIN sold   so ON so.product_id = p.id
		LEFT JOIN tagged tg ON tg.product_id = p.id
		WHERE p.is_service
		  AND (so.product_id IS NOT NULL OR tg.product_id IS NOT NULL)
		ORDER BY COALESCE(so.revenue,0) - COALESCE(so.cogs,0) - COALESCE(tg.expenses,0) DESC`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to load service profit", err)
	}
	return rows, nil
}
