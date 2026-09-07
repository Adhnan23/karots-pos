package aicatalog

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/features/products"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// MockRow is one staging (draft) product. It becomes a real product on commit;
// a committed row keeps created_product_id so the commit can be reverted.
type MockRow struct {
	ID               int64  `db:"id"`
	Name             string `db:"name"`
	Detail           string `db:"detail"`
	Qty              string `db:"qty"`
	Barcode          string `db:"barcode"`
	CostPrice        string `db:"cost_price"`
	SellingPrice     string `db:"selling_price"`
	Category         string `db:"category"`
	Specs            string `db:"specs"`
	Explanation      string `db:"explanation"`
	Confident        bool   `db:"confident"`
	Status           string `db:"status"`
	CreatedProductID *int64 `db:"created_product_id"`
}

// MockData is the staging view-model rendered by MockSection.
type MockData struct {
	Drafts       []MockRow
	Committed    []MockRow
	Markup       string
	ManualMode   bool
	EnrichPrompt string // non-empty => show the paste-your-chatbot-reply box
	EnrichError  string
}

const mockCols = `id, name, detail, qty, barcode, cost_price, selling_price,
	category, specs, explanation, confident, status, created_product_id`

// mockEditable are the columns a table cell may autosave (draft rows only).
var mockEditable = map[string]bool{
	"name": true, "detail": true, "qty": true, "barcode": true,
	"cost_price": true, "selling_price": true, "category": true,
}

func (s *Store) InsertMock(ctx context.Context, r MockRow) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id, `
		INSERT INTO aicatalog_mock_products (name, detail, qty, barcode, cost_price, selling_price)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		r.Name, r.Detail, r.Qty, r.Barcode, r.CostPrice, r.SellingPrice)
	return id, err
}

func (s *Store) GetMock(ctx context.Context, id int64) (*MockRow, error) {
	var r MockRow
	err := s.db.GetContext(ctx, &r, `SELECT `+mockCols+` FROM aicatalog_mock_products WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListMockByStatus(ctx context.Context, status string) ([]MockRow, error) {
	var rows []MockRow
	err := s.db.SelectContext(ctx, &rows,
		`SELECT `+mockCols+` FROM aicatalog_mock_products WHERE status=$1 ORDER BY id`, status)
	return rows, err
}

// UpdateMockField sets one whitelisted column on a draft row.
func (s *Store) UpdateMockField(ctx context.Context, id int64, col, val string) error {
	if !mockEditable[col] {
		return nil
	}
	// col is whitelisted above, so this interpolation is safe.
	_, err := s.db.ExecContext(ctx,
		`UPDATE aicatalog_mock_products SET `+col+`=$2, updated_at=now() WHERE id=$1 AND status='draft'`,
		id, val)
	return err
}

func (s *Store) DeleteMock(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM aicatalog_mock_products WHERE id=$1 AND status='draft'`, id)
	return err
}

func (s *Store) DeleteMockByStatus(ctx context.Context, status string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM aicatalog_mock_products WHERE status=$1`, status)
	return err
}

// ApplyEnrich folds AI results into draft rows, matched by mock id. A blank name
// keeps the typed one. Rows not in the results are left untouched.
func (s *Store) ApplyEnrich(ctx context.Context, results []EnrichResult) error {
	for _, r := range results {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE aicatalog_mock_products SET
			  name = CASE WHEN $2 <> '' THEN $2 ELSE name END,
			  category=$3, specs=$4, explanation=$5, confident=$6, updated_at=now()
			WHERE id=$1 AND status='draft'`,
			r.ID, strings.TrimSpace(r.Name), r.Category, r.Specs, r.Explanation, r.Confident); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) MarkMockCommitted(ctx context.Context, id, productID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE aicatalog_mock_products SET status='committed', created_product_id=$2, updated_at=now() WHERE id=$1`,
		id, productID)
	return err
}

func (s *Store) RestoreMockDraft(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE aicatalog_mock_products SET status='draft', created_product_id=NULL, updated_at=now() WHERE id=$1`, id)
	return err
}

// --- handlers ---

// mockData loads the staging view-model shared by Page and every mock fragment.
func (a *adminUI) mockData(ctx context.Context) (MockData, error) {
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return MockData{}, err
	}
	drafts, err := a.p.store.ListMockByStatus(ctx, "draft")
	if err != nil {
		return MockData{}, err
	}
	committed, err := a.p.store.ListMockByStatus(ctx, "committed")
	if err != nil {
		return MockData{}, err
	}
	return MockData{
		Drafts: drafts, Committed: committed,
		Markup: cfg.DefaultMarkup.String(), ManualMode: cfg.ManualMode,
	}, nil
}

// MockAddRow inserts one draft and returns its table row (appended client-side),
// keeping the add form focused for rapid entry.
func (a *adminUI) MockAddRow(c echo.Context) error {
	ctx := c.Request().Context()
	r := MockRow{
		Name:         strings.TrimSpace(c.FormValue("name")),
		Detail:       strings.TrimSpace(c.FormValue("detail")),
		Qty:          strings.TrimSpace(c.FormValue("qty")),
		Barcode:      strings.TrimSpace(c.FormValue("barcode")),
		CostPrice:    strings.TrimSpace(c.FormValue("cost_price")),
		SellingPrice: strings.TrimSpace(c.FormValue("selling_price")),
	}
	if r.Name == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("Type a name first.", "error"))
		return response.NoContent(c)
	}
	id, err := a.p.store.InsertMock(ctx, r)
	if err != nil {
		return err
	}
	r.ID = id
	r.Status = "draft"
	return response.RenderFragment(c, MockRowTR(r))
}

// MockUpdateRow autosaves whichever whitelisted cell changed.
func (a *adminUI) MockUpdateRow(c echo.Context) error {
	ctx := c.Request().Context()
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	form, err := c.FormParams()
	if err != nil {
		return err
	}
	for col := range mockEditable {
		if vals, ok := form[col]; ok && len(vals) > 0 {
			if e := a.p.store.UpdateMockField(ctx, id, col, strings.TrimSpace(vals[0])); e != nil {
				return e
			}
		}
	}
	return response.NoContent(c)
}

func (a *adminUI) MockDeleteRow(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := a.p.store.DeleteMock(c.Request().Context(), id); err != nil {
		return err
	}
	// 200 (not 204) so htmx performs the hx-swap="delete" that removes the row.
	return c.NoContent(http.StatusOK)
}

// mockRowURL is the autosave/delete endpoint for one draft row.
func mockRowURL(id int64) string { return "/admin/ai-catalog/mock/row/" + itoa64(id) }

// MockEnrich runs AI over the current drafts — API mode fills them directly;
// manual mode renders a prompt to paste into a chatbot.
func (a *adminUI) MockEnrich(c echo.Context) error {
	ctx := c.Request().Context()
	md, err := a.mockData(ctx)
	if err != nil {
		return err
	}
	if len(md.Drafts) == 0 {
		c.Response().Header().Set("HX-Trigger", response.Toast("Add some rows first.", "error"))
		return response.RenderFragment(c, MockSection(md))
	}
	cats, _ := a.p.store.CategoryPaths(ctx)
	rows := enrichRows(md.Drafts)
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if cfg.ManualMode {
		md.EnrichPrompt = EnrichPrompt(rows, cats)
		return response.RenderFragment(c, MockSection(md))
	}
	res, aerr := NewClient(cfg).EnrichBatch(ctx, rows, cats)
	if aerr != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("AI failed: "+aerr.Error(), "error"))
		return response.RenderFragment(c, MockSection(md))
	}
	if err := a.p.store.ApplyEnrich(ctx, res); err != nil {
		return err
	}
	md, _ = a.mockData(ctx)
	return response.RenderFragment(c, MockSection(md),
		response.Toast("Enriched "+itoa(len(res))+" item(s)", "success"))
}

// MockEnrichApply reads the JSON the owner pasted from their chatbot.
func (a *adminUI) MockEnrichApply(c echo.Context) error {
	ctx := c.Request().Context()
	res, perr := parseEnrich([]byte(c.FormValue("response")))
	if perr != nil {
		md, err := a.mockData(ctx)
		if err != nil {
			return err
		}
		cats, _ := a.p.store.CategoryPaths(ctx)
		md.EnrichPrompt = EnrichPrompt(enrichRows(md.Drafts), cats)
		md.EnrichError = "Couldn't read that — paste the JSON array (starts with [)."
		return response.RenderFragment(c, MockSection(md))
	}
	if err := a.p.store.ApplyEnrich(ctx, res); err != nil {
		return err
	}
	md, err := a.mockData(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, MockSection(md),
		response.Toast("Applied "+itoa(len(res))+" item(s)", "success"))
}

// MockCommit turns every draft into a real product (dup-guarded), keeping the
// staging row as "committed" so the commit can be reverted.
func (a *adminUI) MockCommit(c echo.Context) error {
	ctx := c.Request().Context()
	drafts, err := a.p.store.ListMockByStatus(ctx, "draft")
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)
	created, skipped := 0, 0
	seen := map[string]bool{}
	for _, d := range drafts {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			skipped++
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			skipped++
			continue
		}
		seen[key] = true
		// Skip a name that already exists in the real catalog (no duplicates).
		if existing, ferr := a.p.core.Products.FindByName(ctx, name); ferr == nil && existing != nil {
			skipped++
			continue
		}
		catPath := strings.ReplaceAll(d.Category, "/", ">")
		catID, cerr := a.p.cats.FindOrCreateByPath(ctx, catPath)
		if cerr != nil || catID <= 0 {
			if catID, cerr = a.p.cats.FindOrCreateByPath(ctx, "Uncategorized"); cerr != nil {
				return cerr
			}
		}
		unitID, uerr := a.defaultUnitID(ctx)
		if uerr != nil {
			return uerr
		}
		barcode := strings.TrimSpace(d.Barcode)
		if barcode != "" {
			if _, e := a.p.core.Products.GetByBarcode(ctx, barcode); e == nil {
				barcode = "" // taken — generate a fresh one
			}
		}
		if barcode == "" {
			if barcode, err = a.p.core.Products.GenerateBarcode(ctx); err != nil {
				return err
			}
		}
		p, perr := a.p.core.Products.Create(ctx, products.CreateInput{
			Name:         name,
			Barcode:      strPtr(barcode),
			CategoryID:   catID,
			UnitID:       unitID,
			CostPrice:    d.CostPrice,
			SellingPrice: d.SellingPrice,
		})
		if perr != nil {
			skipped++
			continue
		}
		if n, e := decimal.NewFromString(d.Qty); e == nil && n.IsPositive() {
			_ = a.p.core.Stock.Adjust(ctx, stock.AdjustInput{
				ProductID: p.ID, NewQuantity: n.String(),
				Note: "AI mock commit", SellingPrice: d.SellingPrice,
			}, userID)
		}
		_ = a.p.store.LearnItem(ctx, LearnedItem{
			ProductID: p.ID, ResolvedName: name, SuggestedCategory: d.Category,
			Specs: d.Specs, UserExplanation: d.Detail, Source: "mock_commit", RawQuery: name,
		})
		_ = a.p.store.MarkMockCommitted(ctx, d.ID, p.ID)
		created++
	}
	md, err := a.mockData(ctx)
	if err != nil {
		return err
	}
	msg := "Created " + itoa(created)
	if skipped > 0 {
		msg += ", skipped " + itoa(skipped) + " duplicate(s)"
	}
	return response.RenderFragment(c, MockSection(md), response.Toast(msg, "success"))
}

// MockRevert backs out the committed products (stock reversed + soft-disabled)
// and restores their staging rows to draft.
func (a *adminUI) MockRevert(c echo.Context) error {
	ctx := c.Request().Context()
	committed, err := a.p.store.ListMockByStatus(ctx, "committed")
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)
	n := 0
	for _, d := range committed {
		if d.CreatedProductID != nil {
			pid := *d.CreatedProductID
			if p, gerr := a.p.core.Products.Get(ctx, pid); gerr == nil && p != nil {
				if delta, e := decimal.NewFromString(d.Qty); e == nil && delta.IsPositive() {
					newQty := p.StockQty.Sub(delta)
					if newQty.IsNegative() {
						newQty = decimal.Zero
					}
					_ = a.p.core.Stock.Adjust(ctx, stock.AdjustInput{
						ProductID: pid, NewQuantity: newQty.String(), Note: "AI mock revert",
					}, userID)
				}
			}
			_ = a.p.core.Products.Delete(ctx, pid)
		}
		_ = a.p.store.RestoreMockDraft(ctx, d.ID)
		n++
	}
	md, err := a.mockData(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, MockSection(md),
		response.Toast("Reverted "+itoa(n)+" product(s) back to drafts", "success"))
}

// MockClear drops the committed staging rows once the owner is happy (the real
// products stay).
func (a *adminUI) MockClear(c echo.Context) error {
	ctx := c.Request().Context()
	if err := a.p.store.DeleteMockByStatus(ctx, "committed"); err != nil {
		return err
	}
	md, err := a.mockData(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, MockSection(md), response.Toast("Cleared committed rows", "success"))
}

func enrichRows(drafts []MockRow) []EnrichRow {
	out := make([]EnrichRow, 0, len(drafts))
	for _, d := range drafts {
		out = append(out, EnrichRow{ID: d.ID, Name: d.Name, Detail: d.Detail})
	}
	return out
}
