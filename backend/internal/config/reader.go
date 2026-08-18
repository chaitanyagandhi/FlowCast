package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// reader pulls typed values out of the environment while accumulating problems instead of
// returning early. Callers build the whole Config, then check reader.errs once.
//
// An unset or blank variable always falls back to the supplied default; only a present but
// unparseable value is an error.
type reader struct {
	errs []FieldError
}

func (r *reader) add(variable, message string) {
	r.errs = append(r.errs, FieldError{Variable: variable, Message: message})
}

// lookup returns the trimmed value of key and whether it was set to something non-blank.
func lookup(key string) (string, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	return v, v != ""
}

func (r *reader) str(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

func (r *reader) requiredStr(key string) string {
	v, ok := lookup(key)
	if !ok {
		r.add(key, "is required")
	}
	return v
}

func (r *reader) intVal(key string, def int) int {
	raw, ok := lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		r.add(key, fmt.Sprintf("must be an integer; got %q", raw))
		return def
	}
	return n
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	raw, ok := lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		r.add(key, fmt.Sprintf("must be a duration such as 15m or 24h; got %q", raw))
		return def
	}
	return d
}

func (r *reader) logLevel(key string, def slog.Level) slog.Level {
	raw, ok := lookup(key)
	if !ok {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(raw)); err != nil {
		r.add(key, fmt.Sprintf("must be one of debug, info, warn, error; got %q", raw))
		return def
	}
	return lvl
}

// strSlice reads a comma-separated list, dropping blank entries.
func (r *reader) strSlice(key string, def []string) []string {
	raw, ok := lookup(key)
	if !ok {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}
