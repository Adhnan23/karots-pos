// Package response centralizes the two output shapes the app produces: JSON
// envelopes (API handlers) and rendered Templ components (UI handlers).
package response

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type Envelope[T any] struct {
	Success bool   `json:"success"`
	Data    T      `json:"data,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PageMeta struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type PagedEnvelope[T any] struct {
	Success bool     `json:"success"`
	Data    []T      `json:"data"`
	Meta    PageMeta `json:"meta"`
}

func OK[T any](c echo.Context, data T) error {
	return c.JSON(http.StatusOK, Envelope[T]{Success: true, Data: data})
}

func Created[T any](c echo.Context, data T) error {
	return c.JSON(http.StatusCreated, Envelope[T]{Success: true, Data: data})
}

func Paged[T any](c echo.Context, data []T, meta PageMeta) error {
	if data == nil {
		data = []T{}
	}
	return c.JSON(http.StatusOK, PagedEnvelope[T]{Success: true, Data: data, Meta: meta})
}

func NoContent(c echo.Context) error {
	return c.NoContent(http.StatusNoContent)
}

// NewPageMeta computes pagination metadata, guarding against a zero limit.
func NewPageMeta(page, limit, total int) PageMeta {
	if limit <= 0 {
		limit = 1
	}
	return PageMeta{
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: (total + limit - 1) / limit,
	}
}
