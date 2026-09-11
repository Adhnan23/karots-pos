// Package repairs tracks device repair jobs from drop-off to pickup and settles
// the money as a normal core sale. Core never imports it; it attaches only
// through generic plugin hooks and is inert when not built.
package repairs

import (
	"bytes"
	"context"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/datetime"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/plugin"
	"karots-pos/internal/receiptimg"
	"karots-pos/internal/response"
	"karots-pos/plugins/repairs/migrations"
	poststatic "karots-pos/static"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core         plugin.Core
	store        *Store
	labourProdID int64
	defWarranty  int
	warrantyMode string // "days" (whole repair) | "parts" (per-part, PC-shop style)
}

func (p *Plugin) Name() string                { return "Repairs" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "repairs" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	ctx := context.Background()
	labourID, defWarranty, mode, err := p.store.EnsureConfig(ctx, func() (int64, error) {
		return p.ensureLabourProduct(ctx)
	})
	if err == nil {
		p.labourProdID = labourID
		p.defWarranty = defWarranty
		p.warrantyMode = mode
	}
	if p.warrantyMode == "" {
		p.warrantyMode = "days"
	}

	a := &adminUI{p: p}
	reg.Admin().GET("/repairs", a.List)
	reg.Admin().GET("/repairs/new", a.NewForm)
	reg.Admin().POST("/repairs", a.Create)
	reg.Admin().GET("/repairs/report", a.Report)
	reg.Admin().POST("/repairs/config/warranty-mode", a.SetWarrantyMode)
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
	reg.Admin().POST("/repairs/:id/urgent", a.SetUrgent)
	reg.Admin().POST("/repairs/:id/rework", a.WarrantyRework)
	reg.Admin().POST("/repairs/:id/cancel", a.Cancel)
	reg.Admin().GET("/repairs/:id/receipt", a.RepairReceipt)
	reg.Admin().POST("/repairs/:id/print", func(c echo.Context) error { return p.printRepairSlip(c) })

	ch := &cashierUI{p: p}
	reg.Cashier().GET("/repairs/menu", ch.MenuRoot)
	reg.Cashier().GET("/repairs/apply", ch.ApplyForm)
	reg.Cashier().GET("/repairs/find", ch.FindForm)
	reg.Cashier().GET("/repairs/search", ch.SearchJobs)
	reg.Cashier().POST("/repairs", ch.Create)
	reg.Cashier().GET("/repairs/receipts", ch.Receipts)
	reg.Cashier().GET("/repairs/:id", ch.Detail)
	reg.Cashier().POST("/repairs/:id/part", ch.AddPart)
	reg.Cashier().POST("/repairs/:id/part/:pid/delete", ch.RemovePart)
	reg.Cashier().POST("/repairs/:id/charge", ch.AddCharge)
	reg.Cashier().POST("/repairs/:id/charge/:cid/delete", ch.RemoveCharge)
	reg.Cashier().POST("/repairs/:id/urgent", ch.SetUrgent)
	reg.Cashier().POST("/repairs/:id/rework", ch.WarrantyRework)
	reg.Cashier().POST("/repairs/:id/warranty-refund", ch.WarrantyRefund)
	reg.Cashier().POST("/repairs/:id/deposit", ch.TakeDeposit)
	reg.Cashier().POST("/repairs/:id/collect", ch.Collect)
	reg.Cashier().GET("/repairs/:id/receipt", ch.RepairReceipt)
	reg.Cashier().POST("/repairs/:id/print", func(c echo.Context) error { return p.printRepairSlip(c) })

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

// repairSlipESCPOS builds the repair slip as ESC/POS bytes using the shared
// escpos primitives — the same server-side, raw, no-browser path every other
// receipt uses (see internal/web buildReceiptSlip). Header logo/raster is left
// to the sale receipt; this text slip carries the repair detail.
func repairSlipESCPOS(cfg settings.Settings, d *Detail, sym string, opts escpos.Options, mode string) []byte {
	w := escpos.Columns(cfg.ReceiptWidth)
	var b bytes.Buffer
	escpos.Init(&b)
	escpos.Header(&b, cfg, opts)
	escpos.Title(&b, cfg, "REPAIR", w)
	escpos.Left(&b)
	escpos.Divider(&b, cfg, w)
	escpos.Line(&b, escpos.LeftRight("Ticket:", d.Job.TicketNo, w))
	escpos.Line(&b, escpos.LeftRight("Date:", datetime.DateTime(d.Job.CreatedAt), w))
	if dev := strings.TrimSpace(d.Job.RepairType + " " + d.Job.DeviceModel); dev != "" {
		escpos.Line(&b, escpos.ASCII(dev))
	}
	if d.Job.Fault != "" {
		for _, ln := range escpos.Wrap(escpos.ASCII("Fault: "+d.Job.Fault), w) {
			escpos.Line(&b, ln)
		}
	}
	if d.Job.CustomerName != "" {
		escpos.Line(&b, escpos.LeftRight("Customer:", escpos.ASCII(d.Job.CustomerName), w))
	}
	if mode != "parts" && d.Job.WarrantyUntil != nil {
		escpos.Line(&b, escpos.LeftRight("Warranty until:", datetime.Date(*d.Job.WarrantyUntil), w))
	}
	escpos.Divider(&b, cfg, w)
	for _, p := range d.Parts {
		name := p.ProductName
		if mode == "parts" && p.WarrantyDays > 0 {
			name += " (" + strconv.Itoa(p.WarrantyDays) + "d warranty)"
		}
		escpos.Line(&b, escpos.ASCII(name))
		net := p.Qty.Mul(p.UnitCharge).Sub(p.Discount)
		escpos.Line(&b, escpos.LeftRight("  "+money.Display(p.Qty)+" x "+money.Display(p.UnitCharge), money.Display(net), w))
	}
	for _, ch := range d.Charges {
		escpos.Line(&b, escpos.LeftRight(escpos.ASCII(ch.Label), money.Display(ch.Amount), w))
	}
	escpos.Divider(&b, cfg, w)
	total, dep, bal := JobTotals(d)
	escpos.Emphasis(&b, true)
	escpos.Line(&b, escpos.LeftRight("TOTAL", money.Format(sym, total), w))
	escpos.Emphasis(&b, false)
	if dep.IsPositive() {
		escpos.Line(&b, escpos.LeftRight("Deposit", money.Format(sym, dep), w))
		escpos.Line(&b, escpos.LeftRight("Balance", money.Format(sym, bal), w))
	}
	escpos.Footer(&b, cfg)
	return b.Bytes()
}

// advanceSlipESCPOS builds a compact deposit/advance receipt (server-side
// ESC/POS). Given to the customer when they pay an advance at drop-off, before
// the job is priced — so the job total may still be zero (shown only once set).
func advanceSlipESCPOS(cfg settings.Settings, d *Detail, sym string, amount decimal.Decimal, opts escpos.Options) []byte {
	w := escpos.Columns(cfg.ReceiptWidth)
	var b bytes.Buffer
	escpos.Init(&b)
	escpos.Header(&b, cfg, opts)
	escpos.Title(&b, cfg, "DEPOSIT", w)
	escpos.Left(&b)
	escpos.Divider(&b, cfg, w)
	escpos.Line(&b, escpos.LeftRight("Ticket:", d.Job.TicketNo, w))
	escpos.Line(&b, escpos.LeftRight("Date:", datetime.DateTime(time.Now()), w))
	if dev := strings.TrimSpace(d.Job.RepairType + " " + d.Job.DeviceModel); dev != "" {
		escpos.Line(&b, escpos.ASCII(dev))
	}
	if d.Job.CustomerName != "" {
		escpos.Line(&b, escpos.LeftRight("Customer:", escpos.ASCII(d.Job.CustomerName), w))
	}
	escpos.Divider(&b, cfg, w)
	total, dep, bal := JobTotals(d)
	escpos.Emphasis(&b, true)
	escpos.Line(&b, escpos.LeftRight("DEPOSIT PAID", money.Format(sym, amount), w))
	escpos.Emphasis(&b, false)
	if total.IsPositive() {
		escpos.Line(&b, escpos.LeftRight("Job total", money.Format(sym, total), w))
		escpos.Line(&b, escpos.LeftRight("Paid to date", money.Format(sym, dep), w))
		escpos.Line(&b, escpos.LeftRight("Balance", money.Format(sym, bal), w))
	}
	escpos.Footer(&b, cfg)
	return b.Bytes()
}

// printAdvanceSlip prints the deposit receipt, folding the drawer kick into the
// same job (one pass → drawer pops as the slip prints, for a cash advance).
// Best-effort: a printer error must never fail the deposit that already booked.
func (p *Plugin) printAdvanceSlip(ctx context.Context, userID int64, d *Detail, amount decimal.Decimal) {
	cfg, err := p.core.Settings.Get(ctx)
	if err != nil || cfg == nil {
		return
	}
	sym := "Rs."
	if cfg.CurrencySymbol != "" {
		sym = cfg.CurrencySymbol
	}
	opts := receiptimg.SlipOptions(ctx, cfg, poststatic.Files)
	slip := append(escpos.DrawerKick(*cfg), advanceSlipESCPOS(*cfg, d, sym, amount, opts)...)
	_ = escpos.Send(ctx, p.receiptQueue(ctx, userID), slip)
}

// kickDrawer pops the cash drawer (setting-gated, default off) via the cashier's
// printer — used when cash lands in the drawer with no slip of its own.
func (p *Plugin) kickDrawer(ctx context.Context, userID int64) {
	if cfg, err := p.core.Settings.Get(ctx); err == nil && cfg != nil {
		if b := escpos.DrawerKick(*cfg); b != nil {
			_ = escpos.Send(ctx, p.receiptQueue(ctx, userID), b)
		}
	}
}

// startRework opens a free warranty re-repair: a new job linked to the original
// (rework_of), copying who/what, for a device that failed within its warranty
// days. It carries no new warranty period of its own — the original's still runs.
func (p *Plugin) startRework(ctx context.Context, origID, userID int64) (int64, error) {
	o, err := p.store.GetJob(ctx, origID)
	if err != nil {
		return 0, apperr.NotFound("repair")
	}
	if o.Job.Status != "collected" {
		return 0, apperr.Validation("only a collected repair can be re-repaired under warranty")
	}
	if o.Job.WarrantyUntil != nil && time.Now().After(*o.Job.WarrantyUntil) {
		return 0, apperr.Validation("this repair is out of warranty")
	}
	return p.store.CreateJob(ctx, JobInput{
		CustomerID: o.Job.CustomerID, CustomerName: o.Job.CustomerName, CustomerPhone: o.Job.CustomerPhone,
		RepairType: o.Job.RepairType, DeviceModel: o.Job.DeviceModel, RepairedBy: o.Job.RepairedBy,
		Fault:        "Warranty re-repair of " + o.Job.TicketNo,
		ReworkOf:     &origID,
		WarrantyDays: 0, // the original's warranty still governs; don't start a new one
		CreatedBy:    userID,
	})
}

// auditDeadline records a pickup-deadline change in the core audit log (shows up
// in the unified Activity view) — accountability for "you promised it sooner"
// disputes. No-op when the date is unchanged or cleared.
func (p *Plugin) auditDeadline(ctx context.Context, userID, id int64, oldP, newP *time.Time, ticket string) {
	if newP == nil {
		return
	}
	if oldP != nil && oldP.Truncate(24*time.Hour).Equal(newP.Truncate(24*time.Hour)) {
		return
	}
	msg := "set pickup deadline " + datetime.Date(*newP)
	if oldP != nil {
		msg = "moved pickup deadline " + datetime.Date(*oldP) + " -> " + datetime.Date(*newP)
	}
	p.core.Audit.Record(ctx, userID, audit.ActionUpdate, "repair", strconv.FormatInt(id, 10), msg+" ("+ticket+")")
}

// receiptQueue resolves the per-cashier printer target: the user's own account
// printer, else the shop-wide setting (mirrors the core receiptQueue without the
// auth service — the plugin only has Core.DB + Core.Settings).
func (p *Plugin) receiptQueue(ctx context.Context, userID int64) string {
	var pr string
	if userID != 0 {
		_ = p.core.DB.GetContext(ctx, &pr, `SELECT COALESCE(receipt_printer,'') FROM users WHERE id = $1`, userID)
		if strings.TrimSpace(pr) != "" {
			return pr
		}
	}
	if cfg, err := p.core.Settings.Get(ctx); err == nil && cfg != nil {
		return cfg.ReceiptPrinter
	}
	return ""
}

// printRepairSlip sends the repair slip to the resolved printer as raw ESC/POS —
// the POST target of the receipt view's Print button (not window.print).
func (p *Plugin) printRepairSlip(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	d, err := p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	cfg, err := p.core.Settings.Get(ctx)
	if err != nil {
		return err
	}
	eff := *cfg
	switch c.QueryParam("size") {
	case "58":
		eff.ReceiptWidth = "58mm"
	case "80":
		eff.ReceiptWidth = "80mm"
	}
	sym := "Rs."
	if eff.CurrencySymbol != "" {
		sym = eff.CurrencySymbol
	}
	opts := receiptimg.SlipOptions(ctx, &eff, poststatic.Files)
	// kind=advance reprints the drop-off deposit slip (amount carried in the
	// query); anything else is the full repair slip.
	slip := repairSlipESCPOS(eff, d, sym, opts, p.warrantyMode)
	label := "Repair slip"
	if c.QueryParam("kind") == "advance" {
		amt, _ := money.Parse(c.QueryParam("amount"))
		slip = advanceSlipESCPOS(eff, d, sym, amt, opts)
		label = "Deposit slip"
	}
	if err := escpos.Send(ctx, p.receiptQueue(ctx, middleware.CurrentUserID(c)), slip); err != nil {
		return apperr.Internal("could not print slip", err)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast(label+" sent to printer", "success"))
	return response.OK(c, map[string]bool{"ok": true})
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
		warranty = datetime.Date(*d.Job.WarrantyUntil)
	}
	narrow := c.QueryParam("size") == "58"
	curSize, switchSize, switchText := "80", "58", "Switch to 58mm"
	if narrow {
		curSize, switchSize, switchText = "58", "80", "Switch to 80mm"
	}
	return response.RenderPage(c, RepairReceipt(ReceiptData{
		Symbol: sym, ShopName: shop, Address: addr, Phone: phone, Footer: footer, D: d,
		Total: money.Format(sym, total), Deposit: money.Format(sym, dep),
		Balance: money.Format(sym, bal), WarrantyLabel: warranty, WarrantyMode: p.warrantyMode,
		Narrow:     narrow,
		SwitchURL:  c.Request().URL.Path + "?size=" + switchSize,
		SwitchText: switchText,
		PrintURL:   strings.Replace(c.Request().URL.Path, "/receipt", "/print", 1) + "?size=" + curSize,
	}))
}

// collect settles a job as a core sale: parts + charge lines, with the held
// deposit applied as a non-cash wallet tender and the balance as `method`. It
// links the sale, flips the status to collected, and stamps the warranty.
func (p *Plugin) collect(ctx context.Context, jobID int64, method, payNowStr string, userID int64) (cashPaid, onAccount decimal.Decimal, err error) {
	d, err := p.store.GetJob(ctx, jobID)
	if err != nil {
		return decimal.Zero, decimal.Zero, apperr.NotFound("repair")
	}
	if d.Job.Status == "collected" {
		return decimal.Zero, decimal.Zero, apperr.Validation("this repair is already collected")
	}
	if len(d.Parts) == 0 && len(d.Charges) == 0 {
		return decimal.Zero, decimal.Zero, apperr.Validation("add a part or a charge before collecting")
	}

	// Warranty re-repair (a job linked to an earlier one): the customer pays
	// nothing. Ring a zero-priced sale so the redo's parts still leave stock and
	// their cost is booked as a loss, then close it. No deposit/tenders involved.
	if d.Job.ReworkOf != nil {
		in := BuildCollectionSale(d, p.labourProdID, d.Job.CustomerID, nil, true)
		in.AllowOverLimit = true
		sale, serr := p.core.Sales.Create(ctx, in, userID)
		if serr != nil {
			return decimal.Zero, decimal.Zero, serr
		}
		if serr := p.store.MarkCollected(ctx, jobID, sale.Sale.ID, nil); serr != nil {
			return decimal.Zero, decimal.Zero, serr
		}
		p.core.Audit.Record(ctx, userID, audit.ActionUpdate, "repair",
			strconv.FormatInt(jobID, 10), "closed warranty re-repair (free, sale "+strconv.FormatInt(sale.Sale.ID, 10)+")")
		return decimal.Zero, decimal.Zero, nil
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
			return decimal.Zero, decimal.Zero, apperr.Validation("amount is invalid")
		}
		if v.GreaterThan(balance) {
			v = balance
		}
		payNow = v
	}
	onAccount = balance.Sub(payNow)
	if onAccount.IsPositive() && d.Job.CustomerID == nil {
		return decimal.Zero, decimal.Zero, apperr.Validation("choose a registered customer to leave a balance on account")
	}

	in := BuildCollectionSale(d, p.labourProdID, d.Job.CustomerID,
		collectionTenders(depositPaid, payNow, onAccount, method), false)
	in.AllowOverLimit = true // an owner collecting a finished repair isn't blocked by a credit limit
	sale, err := p.core.Sales.Create(ctx, in, userID)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	if err := p.store.MarkCollected(ctx, jobID, sale.Sale.ID, jobWarrantyUntil(p.warrantyMode, d, time.Now())); err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	p.core.Audit.Record(ctx, userID, audit.ActionUpdate, "repair",
		strconv.FormatInt(jobID, 10), "collected repair (sale "+strconv.FormatInt(sale.Sale.ID, 10)+")")
	// Only cash handed over now lands in the drawer; card/online/credit do not.
	if payNow.IsPositive() && method != "card" && method != "online" && method != "credit" {
		cashPaid = payNow
	}
	return cashPaid, onAccount, nil
}
