// Package validator wraps go-playground/validator with a single shared instance
// and turns its raw field errors into a human-readable message attached to an
// apperr.Validation error (status 422).
package validator

import (
	"fmt"
	"strings"

	"karots-pos/internal/apperr"

	"github.com/go-playground/validator/v10"
)

type CustomValidator struct {
	v *validator.Validate
}

func New() *CustomValidator {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Register custom rules here as the domain grows, e.g.:
	// v.RegisterValidation("lkr_phone", validateSriLankanPhone)
	return &CustomValidator{v: v}
}

func (cv *CustomValidator) Validate(i any) error {
	if err := cv.v.Struct(i); err != nil {
		var ve validator.ValidationErrors
		if ok := asValidationErrors(err, &ve); ok {
			return apperr.Validation(humanize(ve))
		}
		return apperr.Validation(err.Error())
	}
	return nil
}

func asValidationErrors(err error, target *validator.ValidationErrors) bool {
	ve, ok := err.(validator.ValidationErrors)
	if ok {
		*target = ve
	}
	return ok
}

func humanize(ve validator.ValidationErrors) string {
	msgs := make([]string, 0, len(ve))
	for _, fe := range ve {
		field := strings.ToLower(fe.Field())
		switch fe.Tag() {
		case "required":
			msgs = append(msgs, fmt.Sprintf("%s is required", field))
		case "min":
			msgs = append(msgs, fmt.Sprintf("%s must be at least %s", field, fe.Param()))
		case "max":
			msgs = append(msgs, fmt.Sprintf("%s must be at most %s", field, fe.Param()))
		case "gt":
			msgs = append(msgs, fmt.Sprintf("%s must be greater than %s", field, fe.Param()))
		case "gte":
			msgs = append(msgs, fmt.Sprintf("%s must be %s or more", field, fe.Param()))
		case "email":
			msgs = append(msgs, fmt.Sprintf("%s must be a valid email", field))
		case "oneof":
			msgs = append(msgs, fmt.Sprintf("%s must be one of: %s", field, fe.Param()))
		default:
			msgs = append(msgs, fmt.Sprintf("%s is invalid", field))
		}
	}
	return strings.Join(msgs, "; ")
}
