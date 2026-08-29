// Package api holds the HTTP conventions shared by middleware and handlers: how a request
// is identified, and how responses and errors are shaped.
//
// It exists as its own package so middleware and handlers can both write a well-formed
// error without importing each other.
package api

import "context"

// RequestIDHeader carries the correlation id on both the request and the response.
const RequestIDHeader = "X-Request-ID"

// MaxRequestIDLen bounds an inbound request id. A caller-supplied value ends up in every
// log line for that request, so it is length-limited and character-checked before use.
const MaxRequestIDLen = 64

type contextKey int

const requestIDKey contextKey = iota

// ContextWithRequestID returns a context carrying the request id.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request id, or an empty string outside a request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ValidRequestID reports whether a caller-supplied id is safe to adopt.
//
// Anything a client sends is echoed into structured logs, so only an unpunctuated ASCII
// token is accepted. This is what stops a crafted header containing newlines from
// injecting forged lines into the log stream.
func ValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > MaxRequestIDLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
