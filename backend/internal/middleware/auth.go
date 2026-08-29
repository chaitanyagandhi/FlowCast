package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
)

// bearerPrefix is the authorization scheme, compared case-insensitively as RFC 7235
// requires.
const bearerPrefix = "bearer "

// Authenticate rejects requests without a valid access token and attaches the caller's
// identity to the request context.
//
// The team the caller belongs to is read from the signed token, never from the request.
// That is the whole point of doing this in middleware: by the time a handler runs, the
// tenant it is allowed to touch has already been decided by something the caller cannot
// forge, so a handler cannot accidentally trust a team id from a path or body.
//
// Access tokens are not checked against the revocation store. They are deliberately
// short-lived, and a Redis round trip on every single request buys at most fifteen minutes
// of earlier cutoff. Revocation applies to refresh tokens, which is what actually ends a
// session.
func Authenticate(tokens *auth.Tokens, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				unauthorized(w, r, logger, api.CodeUnauthorized,
					"Authentication is required.", "missing or malformed authorization header")
				return
			}

			claims, err := tokens.ParseAccess(raw)
			if err != nil {
				if errors.Is(err, auth.ErrTokenExpired) {
					unauthorized(w, r, logger, api.CodeTokenExpired,
						"Your access token has expired.", "expired access token")
					return
				}
				unauthorized(w, r, logger, api.CodeUnauthorized,
					"Authentication is required.", "invalid access token: "+err.Error())
				return
			}

			userID, err := claims.UserID()
			if err != nil {
				unauthorized(w, r, logger, api.CodeUnauthorized,
					"Authentication is required.", "token subject is unusable")
				return
			}

			ctx := api.ContextWithIdentity(r.Context(), api.Identity{
				UserID: userID,
				TeamID: claims.TeamID,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken pulls the credential out of the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if len(header) < len(bearerPrefix) ||
		!strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}

	token := strings.TrimSpace(header[len(bearerPrefix):])
	return token, token != ""
}

// unauthorized answers a rejected request.
//
// The reason is logged, never returned: which of several checks failed is useful to an
// operator and only useful to an attacker.
func unauthorized(w http.ResponseWriter, r *http.Request, logger *slog.Logger,
	code api.ErrorCode, message, reason string) {
	logger.WarnContext(r.Context(), "request not authenticated",
		"reason", reason,
		"method", r.Method,
		"path", r.URL.Path,
		"request_id", api.RequestID(r.Context()))

	// RFC 7235 requires a challenge on a 401. error="invalid_token" is the token-specific
	// code from RFC 6750.
	w.Header().Set("WWW-Authenticate", `Bearer realm="flowcast", error="invalid_token"`)
	api.WriteError(w, r, http.StatusUnauthorized, code, message)
}
