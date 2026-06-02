package web

import (
	"strconv"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/denominations"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// ============================ Denominations ============================

func (a *adminUI) Denominations(c echo.Context) error {
	rows, err := a.s.denominations.List(c.Request().Context(), false)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.DenominationsPage(adminpages.DenominationsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(c.Request().Context()),
		Rows:     rows,
	}))
}

func (a *adminUI) DenominationsTable(c echo.Context) error {
	rows, err := a.s.denominations.List(c.Request().Context(), false)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.DenominationRows(rows, a.symbol(c.Request().Context())))
}

func (a *adminUI) DenominationForm(c echo.Context) error {
	ctx := c.Request().Context()
	var cur *denominations.Denomination
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if cur, err = a.s.denominations.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.DenominationForm(cur))
}

func (a *adminUI) DenominationCreate(c echo.Context) error {
	var in denominations.Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.denominations.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Denomination added", "reload-denoms")
}

func (a *adminUI) DenominationUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in denominations.Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.denominations.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Denomination updated", "reload-denoms")
}

func (a *adminUI) DenominationDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.denominations.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return htmxReload(c, "Denomination removed", "reload-denoms")
}

// ============================ Cash register sessions ============================

func (a *adminUI) CashSessions(c echo.Context) error {
	rows, err := a.s.cashRegister.RecentSessions(c.Request().Context(), 100)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.CashSessionsPage(adminpages.CashSessionsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(c.Request().Context()),
		Rows:     rows,
	}))
}

func (a *adminUI) CashSessionDetail(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	sess, moves, err := a.s.cashRegister.SessionDetail(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.CashSessionDetailPage(adminpages.CashSessionDetailData{
		UserName:  middleware.CurrentUserName(c),
		Symbol:    a.symbol(c.Request().Context()),
		Session:   *sess,
		Movements: moves,
	}))
}
