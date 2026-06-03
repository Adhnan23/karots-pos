package web

import (
	"karots-pos/internal/features/audit"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// logAudit records an audit entry for the acting user (best-effort, never fails
// the request). Shared by the admin and cashier UI handlers.
func (s *Server) logAudit(c echo.Context, action, entity, entityID, detail string) {
	if s.audit != nil {
		s.audit.Record(c.Request().Context(), middleware.CurrentUserID(c), action, entity, entityID, detail)
	}
}

// AuditLog shows the activity trail, filterable by entity and date range.
func (a *adminUI) AuditLog(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	entity := c.QueryParam("entity")
	rows, err := a.s.audit.List(ctx, audit.ListFilter{From: &from, To: &to, Entity: entity, Limit: 500})
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.AuditPage(adminpages.AuditData{
		UserName: middleware.CurrentUserName(c),
		From:     fromStr,
		To:       toStr,
		Entity:   entity,
		Rows:     rows,
	}))
}
