package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
)

// Recoverer turns a panicking handler into a 500 instead of a dropped connection, and
// records the stack trace for whoever has to fix it.
//
// The panic value and stack are logged, never sent: they routinely contain table names,
// file paths, and fragments of user data. The client gets the same opaque message as any
// other internal failure, plus the request id that ties it to this log entry.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				// net/http uses this panic to abort a response deliberately; it is a
				// signal to the server, not a bug to report.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				logger.ErrorContext(r.Context(), "panic recovered",
					slog.String("request_id", api.RequestID(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())),
				)

				// If the handler already sent a status line, the response is committed
				// and anything more would corrupt it. The log entry above is all that
				// can be salvaged.
				if written, ok := w.(interface{ Written() bool }); ok && written.Written() {
					return
				}

				api.WriteError(w, r, http.StatusInternalServerError, api.CodeInternal,
					"An unexpected error occurred. Please try again.")
			}()

			next.ServeHTTP(w, r)
		})
	}
}
