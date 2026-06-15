package web

import (
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/recovery"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"
	cashierpages "karots-pos/templates/pages/cashier"

	"github.com/labstack/echo/v4"
)

// warrantyData builds the warranty page model for the admin shell (Base="/admin").
func (a *adminUI) warrantyData(c echo.Context) (cashierpages.WarrantyData, error) {
	ctx := c.Request().Context()
	status := c.QueryParam("status")
	if status == "" {
		status = "all"
	}
	search := c.QueryParam("q")
	units, err := a.s.warranty.List(ctx, status, search)
	if err != nil {
		return cashierpages.WarrantyData{}, err
	}
	return cashierpages.WarrantyData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: true, // admins/managers may always change their PIN
		Symbol:        a.symbol(ctx),
		Base:          "/admin",
		Status:      status,
		Search:      search,
		Units:       units,
	}, nil
}

func (a *adminUI) Warranty(c echo.Context) error {
	d, err := a.warrantyData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.WarrantyAdmin(d))
}

func (a *adminUI) WarrantyTable(c echo.Context) error {
	d, err := a.warrantyData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.WarrantyTable(d))
}

func (a *adminUI) WarrantyLookup(c echo.Context) error {
	ctx := c.Request().Context()
	serial := c.QueryParam("serial")
	if strings.TrimSpace(serial) == "" {
		return response.RenderFragment(c, cashierpages.WarrantyResult(nil, serial, "/admin"))
	}
	detail, err := a.s.warranty.Lookup(ctx, serial)
	if err != nil {
		if ae, ok := apperr.As(err); ok && ae.Status == http.StatusNotFound {
			return response.RenderFragment(c, cashierpages.WarrantyResult(nil, serial, "/admin"))
		}
		return err
	}
	return response.RenderFragment(c, cashierpages.WarrantyResult(detail, serial, "/admin"))
}

func (a *adminUI) WarrantyReplace(c echo.Context) error {
	ctx := c.Request().Context()
	unitID, err := strconv.ParseInt(c.FormValue("unit_id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid unit")
	}
	newSerial := c.FormValue("new_serial")
	reason := c.FormValue("reason")
	oldSerial := c.FormValue("old_serial")

	newUnit, err := a.s.warranty.RecordReplacement(ctx, unitID, newSerial, reason, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "warranty", strconv.FormatInt(unitID, 10), "replaced "+oldSerial+" -> "+newUnit.SerialNo)

	detail, err := a.s.warranty.Lookup(ctx, newUnit.SerialNo)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.WarrantyResult(detail, newUnit.SerialNo, "/admin"),
		response.ToastAnd("Replacement recorded", "success", "reload-warranty"))
}

// ============================ Damage report ============================

func (a *adminUI) damageData(c echo.Context) (adminpages.DamageReportData, error) {
	ctx := c.Request().Context()
	losses, err := a.s.recovery.DamageLosses(ctx)
	if err != nil {
		return adminpages.DamageReportData{}, err
	}
	return adminpages.DamageReportData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Base:     "/admin",
		Losses:   losses,
	}, nil
}

func (a *adminUI) DamageReport(c echo.Context) error {
	d, err := a.damageData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.DamageReport(d))
}

func (a *adminUI) DamageTable(c echo.Context) error {
	d, err := a.damageData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.DamageTable(d))
}

// ============================ Supplier recovery ============================

func (a *adminUI) RecoveryForm(c echo.Context) error {
	ctx := c.Request().Context()
	sourceType := c.QueryParam("source_type")
	sourceID, err := strconv.ParseInt(c.QueryParam("source_id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid loss")
	}
	info, err := a.s.recovery.SourceInfo(ctx, sourceType, sourceID)
	if err != nil {
		return err
	}
	sups, err := a.s.suppliers.List(ctx, "")
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RecoveryForm(adminpages.RecoveryFormData{
		Base:        "/admin",
		Symbol:      a.symbol(ctx),
		SourceType:  sourceType,
		SourceID:    sourceID,
		ProductName: info.ProductName,
		LossValue:   info.LossValue,
		Suppliers:   sups,
	}))
}

func (a *adminUI) RecoveryRecord(c echo.Context) error {
	ctx := c.Request().Context()
	in := recovery.CreateInput{
		SourceType:      c.FormValue("source_type"),
		Outcome:         c.FormValue("outcome"),
		RecoveredAmount: c.FormValue("recovered_amount"),
		Note:            c.FormValue("note"),
	}
	in.SourceID, _ = strconv.ParseInt(c.FormValue("source_id"), 10, 64)
	if sid := c.FormValue("supplier_id"); sid != "" {
		if v, err := strconv.ParseInt(sid, 10, 64); err == nil {
			in.SupplierID = &v
		}
	}
	if err := a.s.recovery.Record(ctx, in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, in.SourceType, strconv.FormatInt(in.SourceID, 10), "supplier recovery: "+in.Outcome)
	return response.RenderFragment(c, adminpages.RecoveryDone(),
		response.ToastAnd("Recovery recorded", "success", "reload-warranty", "reload-damage"))
}
