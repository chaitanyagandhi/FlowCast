package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
)

// ErrorCode is the stable, machine-readable half of an error response. Clients branch on
// this; the human-readable message is free to change.
type ErrorCode string

const (
	CodeBadRequest       ErrorCode = "bad_request"
	CodeValidationFailed ErrorCode = "validation_failed"
	CodeUnauthorized     ErrorCode = "unauthorized"
	CodeForbidden        ErrorCode = "forbidden"
	CodeNotFound         ErrorCode = "not_found"
	CodeMethodNotAllowed ErrorCode = "method_not_allowed"
	CodeConflict         ErrorCode = "conflict"
	CodeRateLimited      ErrorCode = "rate_limited"
	CodeInternal         ErrorCode = "internal_error"
	CodeUnavailable      ErrorCode = "service_unavailable"
)

// ErrorResponse is the single shape every failure takes, so a client never has to guess
// how an error is spelled.
type ErrorResponse struct {
	Error     ErrorCode `json:"error"`
	Message   string    `json:"message"`
	RequestID string    `json:"request_id,omitempty"`

	// Fields carries per-field problems for a rejected payload. Absent otherwise.
	Fields []models.FieldIssue `json:"fields,omitempty"`
}

// genericInternalMessage is what a caller sees when something breaks. Internal error text
// can name tables, hosts, and query fragments, so it is logged and never returned.
const genericInternalMessage = "An unexpected error occurred. Please try again."

// WriteJSON encodes a payload as a JSON response.
//
// The body is encoded before anything is written, so a value that fails to marshal
// produces a clean 500 rather than a truncated body under a 200 status line.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		slog.ErrorContext(r.Context(), "encoding response body",
			"error", err, "request_id", RequestID(r.Context()))
		writeRaw(w, http.StatusInternalServerError, mustEncodeFallback(
			ErrorResponse{
				Error:     CodeInternal,
				Message:   genericInternalMessage,
				RequestID: RequestID(r.Context()),
			}))
		return
	}
	writeRaw(w, status, buf.Bytes())
}

// WriteError sends a structured error with an explicit, caller-safe message.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code ErrorCode, message string) {
	WriteJSON(w, r, status, ErrorResponse{
		Error:     code,
		Message:   message,
		RequestID: RequestID(r.Context()),
	})
}

// WriteInternalError logs the underlying cause and returns a deliberately vague 500. The
// cause reaches the operator through the logs, never the client.
func WriteInternalError(w http.ResponseWriter, r *http.Request, cause error) {
	slog.ErrorContext(r.Context(), "request failed",
		"error", cause,
		"request_id", RequestID(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
	)
	WriteError(w, r, http.StatusInternalServerError, CodeInternal, genericInternalMessage)
}

// WriteValidationError reports every rejected field at once, so a caller can fix a whole
// payload in one round trip. A non-validation error falls back to a plain bad request.
func WriteValidationError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *models.ValidationError
	if !errors.As(err, &verr) {
		WriteError(w, r, http.StatusBadRequest, CodeBadRequest, "The request could not be processed.")
		return
	}

	WriteJSON(w, r, http.StatusUnprocessableEntity, ErrorResponse{
		Error:     CodeValidationFailed,
		Message:   "The request contains invalid fields.",
		RequestID: RequestID(r.Context()),
		Fields:    verr.Issues,
	})
}

// writeRaw sets the headers and body in the one order net/http accepts.
func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Error bodies name resources; keep them out of shared caches.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// mustEncodeFallback encodes the last-resort error body. ErrorResponse is built from
// strings only, so this cannot fail; if it somehow did, an empty object is still valid
// JSON and better than a half-written body.
func mustEncodeFallback(resp ErrorResponse) []byte {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return []byte(`{"error":"internal_error"}`)
	}
	return encoded
}
