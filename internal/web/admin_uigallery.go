package web

import (
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// UIGallery renders the Phase 0b component gallery for visual QA.
func (h *adminUI) UIGallery(c echo.Context) error {
	return response.RenderPage(c, adminpages.UIGallery())
}
