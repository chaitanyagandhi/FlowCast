package middleware

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
)

// RequestID gives every request a correlation id, reachable from the context and echoed
// back on the response so a user reporting a failure can quote something findable.
//
// An inbound X-Request-ID is honoured -- that is how a trace survives a hop between
// services -- but only when it passes api.ValidRequestID. A value that does not is
// replaced rather than rejected: the caller's malformed header is not worth failing an
// otherwise good request over, and adopting it would let arbitrary text into the logs.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(api.RequestIDHeader)
		if !api.ValidRequestID(id) {
			id = uuid.NewString()
		}

		w.Header().Set(api.RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(api.ContextWithRequestID(r.Context(), id)))
	})
}
