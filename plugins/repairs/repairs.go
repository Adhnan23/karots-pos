// Package repairs tracks device repair jobs from drop-off to pickup and settles
// the money as a normal core sale. Core never imports it; it attaches only
// through generic plugin hooks and is inert when not built.
package repairs

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/products"
	"karots-pos/internal/money"
	"karots-pos/internal/plugin"
	"karots-pos/internal/response"
	"karots-pos/plugins/repairs/migrations"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core         plugin.Core
	store        *Store
	labourProdID int64
	defWarranty  int
}

func (p *Plugin) Name() string                { return "Repairs" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "repairs" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	ctx := context.Background()
	labourID, defWarranty, err := p.store.EnsureConfig(ctx, func() (int64, error) {
		return p.ensureLabourProduct(ctx)
	})
	if err == nil {
		p.labourProdID = labourID
		p.defWarranty = defWarranty
	}

	a := &adminUI{p: p}
	reg.Admin().GET("/repairs", a.List)
	reg.Admin().GET("/repairs/new", a.NewForm)
	reg.Admin().POST("/repairs", a.Create)
	reg.Admin().GET("/repairs/report", a.Report)
	reg.Admin().GET("/repairs/ready-count", a.ReadyCount)
	reg.Admin().GET("/repairs/receipts", a.Receipts)
	reg.Admin().GET("/repairs/suggest", a.Suggest)
	reg.Admin().GET("/repairs/:id", a.Detail)
	reg.Admin().POST("/repairs/:id", a.Update)
	reg.Admin().POST("/repairs/:id/part", a.AddPart)
	reg.Admin().POST("/repairs/part/:pid/delete", a.RemovePart)
	reg.Admin().POST("/repairs/:id/charge", a.AddCharge)
	reg.Admin().POST("/repairs/charge/:cid/delete", a.RemoveCharge)
	reg.Admin().POST("/repairs/:id/pay-repairer", a.PayRepairer)
	reg.Admin().POST("/repairs/:id/status", a.SetStatus)
	reg.Admin().POST("/repairs/:id/cancel", a.Cancel)
	reg.Admin().GET("/repairs/:id/receipt", a.RepairReceipt)

	ch := &cashierUI{p: p}
	reg.Cashier().GET("/repairs/menu", ch.MenuRoot)
	reg.Cashier().GET("/repairs/apply", ch.ApplyForm)
	reg.Cashier().POST("/repairs", ch.Create)
	reg.Cashier().GET("/repairs/receipts", ch.Receipts)
	reg.Cashier().GET("/repairs/:id", ch.Detail)
	reg.Cashier().POST("/repairs/:id/part", ch.AddPart)
	reg.Cashier().POST("/repairs/:id/part/:pid/delete", ch.RemovePart)
	reg.Cashier().POST("/repairs/:id/charge", ch.AddCharge)
	reg.Cashier().POST("/repairs/:id/charge/:cid/delete", ch.RemoveCharge)
	reg.Cashier().POST("/repairs/:id/deposit", ch.TakeDeposit)
	reg.Cashier().POST("/repairs/:id/collect", ch.Collect)
	reg.Cashier().GET("/repairs/:id/receipt", ch.RepairReceipt)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Repairs", Icon: "🔧",
		Href: "/admin/repairs", Label: "Repairs", Key: "repairs",
		Desc: "Track repair jobs, parts, warranty",
	})
	reg.AddCashierMenuRoot(plugin.CashierMenuRoot{
		Key: "repairs", Emoji: "🔧", Label: "Repairs", ChildrenURL: "/cashier/repairs/menu",
	})
	reg.AddReceiptTab(plugin.ReceiptTab{
		Key: "repairs", Label: "Repairs",
		CashierHref: "/cashier/repairs/receipts", AdminHref: "/admin/repairs/receipts",
	})
	reg.AddReportCard(plugin.ReportCard{
		Href: "/admin/repairs/report", Label: "🔧 Repairs", Desc: "Repair jobs, revenue & warranty",
	})
	reg.AddActivityContributor(plugin.ActivityContributor{Source: "repairs", List: p.store.ActivityRows})
	reg.AddPaletteEntry(plugin.PaletteEntry{Href: "/admin/repairs", Label: "Repairs", Group: "Repairs"})
	reg.AddDashboardCard(plugin.DashboardCard{Component: ReadyCard()})
}

// ensureLabourProduct returns the hidden is_service product used for labour and
// custom charge lines, creating it once if absent.
func (p *Plugin) ensureLabourProduct(ctx context.Context) (int64, error) {
	if existing, err := p.core.Products.FindByName(ctx, "Repair Service"); err != nil {
		return 0, err
	} else if existing != nil {
		return existing.ID, nil
	}
	catID, unitID, err := p.store.serviceDefaults(ctx)
	if err != nil {
		return 0, err
	}
	prod, err := p.core.Products.Create(ctx, products.CreateInput{
		Name: "Repair Service", CategoryID: catID, UnitID: unitID,
		CostPrice: "0", SellingPrice: "0", WholesalePrice: "0", TaxRate: "0", IsService: true,
	})
	if err != nil {
		return 0, err
	}
	return prod.ID, nil
}

// renderReceipt renders the detailed repair receipt for the :id in the route.
func (p *Plugin) renderReceipt(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	d, err := p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	sym, shop := "Rs.", ""
	var addr, phone, footer *string
	if sc, serr := p.core.Settings.Get(ctx); serr == nil && sc != nil {
		if sc.CurrencySymbol != "" {
			sym = sc.CurrencySymbol
		}
		shop = sc.ShopName
		addr, phone, footer = sc.Address, sc.Phone, sc.ReceiptFooter
	}
	total, dep, bal := JobTotals(d)
	warranty := ""
	if d.Job.WarrantyUntil != nil {
		warranty = d.Job.WarrantyUntil.Format("2006-01-02")
	}
	narrow := c.QueryParam("size") == "58"
	switchSize, switchText := "58", "Switch to 58mm"
	if narrow {
		switchSize, switchText = "80", "Switch to 80mm"
	}
	return response.RenderPage(c, RepairReceipt(ReceiptData{
		Symbol: sym, ShopName: shop, Address: addr, Phone: phone, Footer: footer, D: d,
		Total: money.Format(sym, total), Deposit: money.Format(sym, dep),
		Balance: money.Format(sym, bal), WarrantyLabel: warranty,
		Narrow:     narrow,
		SwitchURL:  c.Request().URL.Path + "?size=" + switchSize,
		SwitchText: switchText,
	}))
}

// collect settles a job as a core sale: parts + charge lines, with the held
// deposit applied as a non-cash wallet tender and the balance as `method`. It
// links the sale, flips the status to collected, and stamps the warranty.
func (p *Plugin) collect(ctx context.Context, jobID int64, method, payNowStr string, userID int64) error {
	d, err := p.store.GetJob(ctx, jobID)
	if err != nil {
		return apperr.NotFound("repair")
	}
	if d.Job.Status == "collected" {
		return apperr.Validation("this repair is already collected")
	}
	if len(d.Parts) == 0 && len(d.Charges) == 0 {
		return apperr.Validation("add a part or a charge before collecting")
	}
	_, depositPaid, balance := JobTotals(d)

	// How much is paid now vs left on account. method "credit" leaves the whole
	// balance on account; a blank pay-now pays it all now; otherwise pay-now is
	// what's handed over and the rest goes on account.
	payNow := balance
	if method == "credit" {
		payNow = decimal.Zero
	} else if raw := strings.TrimSpace(payNowStr); raw != "" {
		v, perr := money.Parse(raw)
		if perr != nil || v.IsNegative() {
			return apperr.Validation("amount is invalid")
		}
		if v.GreaterThan(balance) {
			v = balance
		}
		payNow = v
	}
	onAccount := balance.Sub(payNow)
	if onAccount.IsPositive() && d.Job.CustomerID == nil {
		return apperr.Validation("choose a registered customer to leave a balance on account")
	}

	in := BuildCollectionSale(d, p.labourProdID, d.Job.CustomerID,
		collectionTenders(depositPaid, payNow, onAccount, method))
	in.AllowOverLimit = true // an owner collecting a finished repair isn't blocked by a credit limit
	sale, err := p.core.Sales.Create(ctx, in, userID)
	if err != nil {
		return err
	}
	if err := p.store.MarkCollected(ctx, jobID, sale.Sale.ID, warrantyUntil(d.Job.WarrantyDays, time.Now())); err != nil {
		return err
	}
	p.core.Audit.Record(ctx, userID, audit.ActionUpdate, "repair",
		strconv.FormatInt(jobID, 10), "collected repair (sale "+strconv.FormatInt(sale.Sale.ID, 10)+")")
	return nil
}
