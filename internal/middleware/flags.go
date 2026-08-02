package middleware

import (
	"context"

	"karots-pos/internal/apperr"

	"github.com/labstack/echo/v4"
)

// UserFlags carries per-user permissions that are not expressible as a role.
// It is a plain struct so this package keeps no dependency on the auth feature.
type UserFlags struct {
	// CanHandleSuppliers lets a cashier pay suppliers, take in deliveries and
	// place orders from the till. Meaningless for admins and managers, who may
	// always do so.
	CanHandleSuppliers bool
	// CanManageCredit lets a cashier override a customer's credit limit for one
	// sale and raise a stored credit limit from the till. Meaningless for admins
	// and managers, who may always do so.
	CanManageCredit bool
}

// ctxKey is unexported so nothing outside this package can collide with it.
type ctxKey struct{ name string }

// ctxFlagsKey stashes UserFlags in the *request* context (not just the echo
// context) because response.render hands the request context to templ. That is
// how a template asks whether to draw the Suppliers tab without every cashier
// page having to thread another parameter through its data struct.
var ctxFlagsKey = ctxKey{"user_flags"}

const ctxCanSuppliers = "can_handle_suppliers"

const ctxCanManageCredit = "can_manage_credit"

// CanHandleSuppliers reports the flag for the current request. Admins and
// managers are NOT covered here — this is the raw per-user flag.
func CanHandleSuppliers(c echo.Context) bool {
	b, _ := c.Get(ctxCanSuppliers).(bool)
	return b
}

// CanHandleSuppliersCtx is CanHandleSuppliers for a bare context, for templates.
func CanHandleSuppliersCtx(ctx context.Context) bool {
	f, _ := ctx.Value(ctxFlagsKey).(UserFlags)
	return f.CanHandleSuppliers
}

// MaySeeSuppliers is the full rule: an admin or manager always may; a cashier
// may only with the flag. Used by both the route gate and the nav tab, so the
// tab can never appear on a page the gate would refuse.
func MaySeeSuppliers(role string, flag bool) bool {
	return role == "admin" || role == "manager" || flag
}

// CanManageCredit reports the raw per-user flag for the current request. Admins
// and managers are NOT covered here — this is the raw per-user flag.
func CanManageCredit(c echo.Context) bool {
	b, _ := c.Get(ctxCanManageCredit).(bool)
	return b
}

// CanManageCreditCtx is CanManageCredit for a bare context, for templates.
func CanManageCreditCtx(ctx context.Context) bool {
	f, _ := ctx.Value(ctxFlagsKey).(UserFlags)
	return f.CanManageCredit
}

// MayManageCredit is the full rule: an admin or manager always may; a cashier
// may only with the flag.
func MayManageCredit(role string, flag bool) bool {
	return role == "admin" || role == "manager" || flag
}

// RequireSupplierAccess gates the supplier counter routes. Must run after
// JWTAuth, which is what puts the role and flag in scope.
func RequireSupplierAccess() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role, _ := c.Get(ctxRole).(string)
			if !MaySeeSuppliers(role, CanHandleSuppliers(c)) {
				return apperr.Forbidden("you're not set up to deal with suppliers — ask the owner")
			}
			return next(c)
		}
	}
}

// RequireManageCredit gates the till credit-limit endpoint. Must run after
// JWTAuth, which is what puts the role and flag in scope.
func RequireManageCredit() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role, _ := c.Get(ctxRole).(string)
			if !MayManageCredit(role, CanManageCredit(c)) {
				return apperr.Forbidden("you're not set up to change credit limits — ask the owner")
			}
			return next(c)
		}
	}
}
