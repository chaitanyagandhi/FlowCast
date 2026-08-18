package models

import (
	"fmt"
	"strings"
)

// enum constrains the generic helpers below to string-backed domain enumerations. Every
// enumeration in this package is a string so that it round-trips through JSON, SQL, and
// logs as the same readable token.
type enum interface{ ~string }

// InvalidEnumError reports a value outside an enumeration's allowed set. It names the
// alternatives, so the message is useful straight back to an API caller.
type InvalidEnumError struct {
	Kind    string
	Value   string
	Allowed []string
}

func (e *InvalidEnumError) Error() string {
	return fmt.Sprintf("invalid %s %q: must be one of %s",
		e.Kind, e.Value, strings.Join(e.Allowed, ", "))
}

// validEnum reports whether v is in allowed.
func validEnum[T enum](v T, allowed []T) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// parseEnum converts raw into T, rejecting anything outside allowed.
func parseEnum[T enum](raw, kind string, allowed []T) (T, error) {
	candidate := T(raw)
	if validEnum(candidate, allowed) {
		return candidate, nil
	}
	var zero T
	return zero, &InvalidEnumError{Kind: kind, Value: raw, Allowed: enumStrings(allowed)}
}

// enumStrings renders an enumeration's values for error messages and API documentation.
func enumStrings[T enum](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
