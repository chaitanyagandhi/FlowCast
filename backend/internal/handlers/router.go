package handlers

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/middleware"
)

// Deps is everything the API needs to serve requests. It grows as handlers land, which is
// why it is a struct rather than a widening parameter list.
type Deps struct {
	Config config.ServerConfig
	Logger *slog.Logger

	// Version is reported by the health endpoint so a deployment can be identified.
	Version string

	// HealthChecks are the dependency probes behind GET /health.
	HealthChecks []Check

	// Users backs registration and login.
	Users UserStore
	// Hasher and Tokens are the auth primitives.
	Hasher *auth.Hasher
	Tokens *auth.Tokens
	// SecureCookies marks the refresh cookie Secure. True in production; false locally,
	// where a Secure cookie would not be sent over plain http.
	SecureCookies bool
}

// NewRouter builds the HTTP handler for the whole API.
func NewRouter(deps Deps) http.Handler {
	mux := chi.NewRouter()

	// chi answers unmatched routes and methods with plain text by default. Every
	// FlowCast response is JSON, including the ones nobody planned for.
	mux.NotFound(notFound)
	mux.MethodNotAllowed(methodNotAllowed)

	// Deliberately outside /api/v1 and outside authentication: whatever polls this is
	// an orchestrator, not a logged-in user.
	mux.Method(http.MethodGet, "/health",
		Health(deps.Version, DefaultHealthTimeout, deps.Logger, deps.HealthChecks...))

	mux.Route("/api/v1", func(r chi.Router) {
		if deps.Users != nil {
			authHandler := NewAuthHandler(
				deps.Users, deps.Hasher, deps.Tokens, deps.SecureCookies, deps.Logger)

			r.Route("/auth", func(r chi.Router) {
				r.Post("/register", authHandler.Register)
				r.Post("/login", authHandler.Login)
			})
		}
	})

	return withMiddleware(mux, deps.Config, deps.Logger)
}

// withMiddleware wraps the router in the global middleware chain.
//
// The chain is applied around the mux rather than through chi's Use, for two reasons.
// It makes the ordering explicit at the point of assembly instead of implicit in
// registration order, and it does not depend on the mux having routes: chi skips its
// middleware entirely and answers from NotFoundHandler when no route has been registered,
// which would silently disable logging, recovery, and CORS.
//
// Order matters, outermost first:
//
//  1. RequestID, so every later layer -- the logger, and any error response -- can name
//     the request.
//  2. CORS, so a browser preflight is answered without running anything below it.
//  3. Logger, so it observes the final status of the request, including one that a
//     recovered panic turned into a 500.
//  4. Recoverer, innermost, so the 500 it writes is the status the logger records and no
//     handler panic escapes to drop the connection.
func withMiddleware(handler http.Handler, cfg config.ServerConfig, logger *slog.Logger) http.Handler {
	handler = middleware.Recoverer(logger)(handler)
	handler = middleware.Logger(logger)(handler)
	handler = middleware.CORS(cfg.CORSOrigins)(handler)
	handler = middleware.RequestID(handler)
	return handler
}

func notFound(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, r, http.StatusNotFound, api.CodeNotFound,
		"The requested resource does not exist.")
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, r, http.StatusMethodNotAllowed, api.CodeMethodNotAllowed,
		"That method is not supported for this resource.")
}
