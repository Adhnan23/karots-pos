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
	"github.com/shopspring/decimal"
)

// PL is a profit & loss summary for [From, To).
type PL struct {
	From         time.Time          `json:"from"`
	To           time.Time          `json:"to"`
	SalesCount   int                `json:"sales_count"`
	Revenue      decimal.Decimal    `json:"revenue"`       // sum of sale totals (excl. returned/void)
	Received     decimal.Decimal    `json:"received"`      // cash/card/online actually collected
	COGS         decimal.Decimal    `json:"cogs"`          // cost of goods sold (snapshot cost)
	GrossProfit  decimal.Decimal    `json:"gross_profit"`  // revenue - COGS
	Expenses     decimal.Decimal    `json:"expenses"`      // operating expenses in range
	NetProfit    decimal.Decimal    `json:"net_profit"`    // gross profit - expenses
	Receivables  decimal.Decimal    `json:"receivables"`   // customer dues (current snapshot)
	Payables     decimal.Decimal    `json:"payables"`      // supplier dues (current snapshot)
	CashWithdrawn decimal.Decimal   `json:"cash_withdrawn"` // drawer withdrawals in range
	RegisterDiff  decimal.Decimal   `json:"register_diff"`  // net over/short of sessions closed in range
	TopProducts  []ProductRevenue   `json:"top_products"`
	ByPayment    []MethodTotal      `json:"by_payment"`
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

	var head struct {
		Count   int             `db:"count"`
		Revenue decimal.Decimal `db:"revenue"`
		Paid    decimal.Decimal `db:"paid"`
	}
	if err := s.db.GetContext(ctx, &head, `
		SELECT COUNT(*) AS count,
		       COALESCE(SUM(total),0) AS revenue,
		       COALESCE(SUM(paid_amount),0) AS paid
		FROM sales
		WHERE status IN ('completed','credit') AND created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute revenue", err)
	}
	pl.SalesCount = head.Count
	pl.Revenue = head.Revenue
	pl.Received = head.Paid

	if err := s.db.GetContext(ctx, &pl.COGS, `
		SELECT COALESCE(SUM(si.quantity * si.cost_price),0)
		FROM sale_items si JOIN sales s ON s.id = si.sale_id
		WHERE s.status IN ('completed','credit') AND s.created_at >= $1 AND s.created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute COGS", err)
	}
	pl.GrossProfit = pl.Revenue.Sub(pl.COGS)

	if err := s.db.GetContext(ctx, &pl.Expenses, `
		SELECT COALESCE(SUM(amount),0) FROM expenses
		WHERE expense_date >= $1 AND expense_date < $2`, from, to); err != nil {
		return nil, apperr.Internal("failed to compute expenses", err)
	}
	pl.NetProfit = pl.GrossProfit.Sub(pl.Expenses)

	_ = s.db.GetContext(ctx, &pl.Receivables, `SELECT COALESCE(SUM(outstanding_balance),0) FROM customers`)
	_ = s.db.GetContext(ctx, &pl.Payables, `SELECT COALESCE(SUM(outstanding_balance),0) FROM suppliers`)

	// Cash drawer: total withdrawn and the net over/short of sessions closed in
	// the period (negative = short).
	_ = s.db.GetContext(ctx, &pl.CashWithdrawn, `
		SELECT COALESCE(SUM(ABS(amount)),0) FROM cash_movements
		WHERE type = 'withdrawal' AND created_at >= $1 AND created_at < $2`, from, to)
	_ = s.db.GetContext(ctx, &pl.RegisterDiff, `
		SELECT COALESCE(SUM(difference),0) FROM cash_register
		WHERE closed_at >= $1 AND closed_at < $2`, from, to)

	if err := s.db.SelectContext(ctx, &pl.TopProducts, `
		SELECT p.name AS product_name,
		       SUM(si.quantity) AS qty,
		       SUM(si.subtotal) AS revenue,
		       SUM(si.subtotal - si.quantity * si.cost_price) AS profit
		FROM sale_items si
		JOIN sales s ON s.id = si.sale_id
		JOIN products p ON p.id = si.product_id
		WHERE s.status IN ('completed','credit') AND s.created_at >= $1 AND s.created_at < $2
		GROUP BY p.name
		ORDER BY revenue DESC
		LIMIT 10`, from, to); err != nil {
		return nil, apperr.Internal("failed to compute top products", err)
	}

	if err := s.db.SelectContext(ctx, &pl.ByPayment, `
		SELECT pmt.method, COALESCE(SUM(pmt.amount),0) AS total
		FROM payments pmt JOIN sales s ON s.id = pmt.sale_id
		WHERE s.created_at >= $1 AND s.created_at < $2
		GROUP BY pmt.method ORDER BY total DESC`, from, to); err != nil {
		return nil, apperr.Internal("failed to compute payment breakdown", err)
	}

	return pl, nil
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

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/reports", middleware.JWTAuth(cfg.JWTSecret), middleware.RequireRole("admin", "manager"))
	g.GET("/finance", api.Finance)
}
