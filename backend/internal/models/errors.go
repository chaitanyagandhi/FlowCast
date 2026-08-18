package models

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors the repository and handler layers translate into HTTP responses. They
// live here so no layer has to invent its own vocabulary for "missing" or "duplicate".
var (
	// ErrNotFound means no record matched -- including the case where a record exists
	// but belongs to another team, which callers must not be able to tell apart.
	ErrNotFound = errors.New("not found")

	// ErrConflict means the write collided with an existing record, such as an email
	// that is already registered.
	ErrConflict = errors.New("conflict")
)

// FieldIssue is one problem with one field of a domain object.
type FieldIssue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (f FieldIssue) String() string { return f.Field + " " + f.Message }

// ValidationError collects every problem with a domain object rather than reporting only
// the first, so an API caller can fix a whole form in one round trip.
type ValidationError struct {
	Issues []FieldIssue
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		parts[i] = issue.String()
	}
	return fmt.Sprintf("validation failed: %s", strings.Join(parts, "; "))
}

// validator accumulates field issues while a struct is checked.
type validator struct {
	issues []FieldIssue
}

func (v *validator) add(field, message string) {
	v.issues = append(v.issues, FieldIssue{Field: field, Message: message})
}

// require records an issue when a string is empty after trimming.
func (v *validator) require(field, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		v.add(field, "is required")
	}
	return trimmed
}

// maxLen records an issue when a string is longer than the column allows.
func (v *validator) maxLen(field, value string, max int) {
	if len(value) > max {
		v.add(field, fmt.Sprintf("must be at most %d characters", max))
	}
}

// enumField records an issue when value is outside the allowed set.
func enumField[T enum](v *validator, field string, value T, allowed []T) {
	if !validEnum(value, allowed) {
		v.add(field, fmt.Sprintf("must be one of %s",
			strings.Join(enumStrings(allowed), ", ")))
	}
}

// err returns a *ValidationError, or nil when nothing was recorded.
func (v *validator) err() error {
	if len(v.issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: v.issues}
}
