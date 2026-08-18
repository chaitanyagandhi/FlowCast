package config

import (
	"fmt"
	"sort"
	"strings"
)

// FieldError is a single misconfigured environment variable.
type FieldError struct {
	Variable string
	Message  string
}

func (e FieldError) Error() string {
	return fmt.Sprintf("%s %s", e.Variable, e.Message)
}

// ValidationError collects every configuration problem found during Load so the operator
// sees the complete list rather than fixing one variable at a time.
//
// Its message deliberately contains only variable names and reasons -- never values --
// so it is safe to log or print at startup.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "invalid configuration"
	}

	lines := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		lines[i] = "  - " + f.Error()
	}
	sort.Strings(lines)

	return fmt.Sprintf("invalid configuration (%d problem(s)):\n%s",
		len(e.Fields), strings.Join(lines, "\n"))
}

// Variables returns the names of the offending variables, sorted and deduplicated.
func (e *ValidationError) Variables() []string {
	seen := make(map[string]struct{}, len(e.Fields))
	out := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		if _, dup := seen[f.Variable]; dup {
			continue
		}
		seen[f.Variable] = struct{}{}
		out = append(out, f.Variable)
	}
	sort.Strings(out)
	return out
}
