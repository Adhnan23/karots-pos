// Package apperr defines typed domain errors. Repositories and services return
// these; the error-handling middleware maps them to an HTTP status plus either
// a JSON envelope (API) or an HTML toast (HTMX UI).
package apperr

import (
	"errors"
	"fmt"
)

type AppError struct {
	Code    string
	Message string
	Status  int
	// Err is an optional wrapped cause, surfaced via errors.Unwrap for logging
	// but never shown to clients.
	Err error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *AppError) Unwrap() error { return e.Err }

// As reports whether err is an *AppError, returning it for inspection.
func As(err error) (*AppError, bool) {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

func NotFound(resource string) *AppError {
	return &AppError{Code: "NOT_FOUND", Message: fmt.Sprintf("%s not found", resource), Status: 404}
}

func Validation(msg string) *AppError {
	return &AppError{Code: "VALIDATION_ERROR", Message: msg, Status: 422}
}

func Conflict(msg string) *AppError {
	return &AppError{Code: "CONFLICT", Message: msg, Status: 409}
}

func Unauthorized(msg string) *AppError {
	if msg == "" {
		msg = "authentication required"
	}
	return &AppError{Code: "UNAUTHORIZED", Message: msg, Status: 401}
}

func Forbidden(msg string) *AppError {
	if msg == "" {
		msg = "you do not have permission to do that"
	}
	return &AppError{Code: "FORBIDDEN", Message: msg, Status: 403}
}

func BadRequest(msg string) *AppError {
	return &AppError{Code: "BAD_REQUEST", Message: msg, Status: 400}
}

// Internal wraps an underlying error. The message is safe to show; the cause is
// logged but not exposed.
func Internal(msg string, cause error) *AppError {
	return &AppError{Code: "INTERNAL_ERROR", Message: msg, Status: 500, Err: cause}
}
