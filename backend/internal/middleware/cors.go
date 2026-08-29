package middleware

import (
	"net/http"
	"slices"

	"github.com/go-chi/cors"
)

// corsMaxAgeSeconds is how long a browser may cache a preflight result. Five minutes is
// the effective ceiling in Chrome, so asking for more buys nothing.
const corsMaxAgeSeconds = 300

// CORS allows the configured browser origins to call the API.
//
// Credentials are allowed so the frontend can send its auth cookie, which is why tokens
// are not kept in localStorage. The one exception is a wildcard origin: the spec forbids
// pairing it with credentials, and browsers reject the combination outright, so a
// wildcard turns credentials off rather than producing a config that silently fails.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowCredentials := !slices.Contains(allowedOrigins, "*")

	return cors.Handler(cors.Options{
		AllowedOrigins: allowedOrigins,
		AllowedMethods: []string{
			http.MethodGet, http.MethodPost, http.MethodPatch,
			http.MethodPut, http.MethodDelete, http.MethodOptions,
		},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		// So the frontend can surface the request id when reporting a failure.
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: allowCredentials,
		MaxAge:           corsMaxAgeSeconds,
	})
}
