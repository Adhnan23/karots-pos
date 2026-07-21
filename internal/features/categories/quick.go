package categories

import (
	"strings"

	"karots-pos/internal/apperr"
)

// maxNameLen matches the validate:"max=80" on CreateInput.Name.
const maxNameLen = 80

// CleanName validates a category name typed into the inline creator.
//
// A ">" is rejected rather than split: the parent comes from the row the user
// tapped, so treating the name as a path would quietly create levels they did
// not ask for and could not see.
func CleanName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", apperr.Validation("enter a category name")
	}
	if strings.Contains(name, ">") {
		return "", apperr.Validation("a category name cannot contain '>'")
	}
	if len(name) > maxNameLen {
		return "", apperr.Validation("that category name is too long")
	}
	return name, nil
}
