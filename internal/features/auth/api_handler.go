package auth

import (
	"net/http"
	"strconv"

	"karots-pos/internal/apperr"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) Login(c echo.Context) error {
	var in LoginInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	pair, err := h.svc.Login(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.OK(c, pair)
}

func (h *APIHandler) Refresh(c echo.Context) error {
	var in RefreshInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	pair, err := h.svc.Refresh(c.Request().Context(), in.RefreshToken)
	if err != nil {
		return err
	}
	return response.OK(c, pair)
}

func (h *APIHandler) Logout(c echo.Context) error {
	var in RefreshInput
	_ = c.Bind(&in)
	if err := h.svc.Logout(c.Request().Context(), in.RefreshToken); err != nil {
		return err
	}
	return response.NoContent(c)
}

func (h *APIHandler) ListUsers(c echo.Context) error {
	users, err := h.svc.ListUsers(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, users)
}

func (h *APIHandler) CreateUser(c echo.Context) error {
	var in CreateUserInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	u, err := h.svc.CreateUser(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, u)
}

func (h *APIHandler) DeactivateUser(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.svc.DeactivateUser(c.Request().Context(), id); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
