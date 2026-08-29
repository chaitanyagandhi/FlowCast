package handlers_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/handlers"
)

func sessionTokens(t *testing.T) *auth.Tokens {
	t.Helper()
	tokens, err := auth.NewTokens(config.AuthConfig{
		JWTSecret:       "0123456789abcdef0123456789abcdef-flowcast",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
	})
	require.NoError(t, err)
	return tokens
}

// protectedRouter mounts a probe handler behind the authentication middleware, so the
// wiring can be tested before any real protected endpoint exists.
func protectedRouter(t *testing.T, tokens *auth.Tokens, seen *api.Identity, reached *bool) http.Handler {
	t.Helper()

	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		identity, ok := api.IdentityFrom(r.Context())
		require.True(t, ok, "a protected handler must always have an identity")
		*seen = identity
		w.WriteHeader(http.StatusOK)
	})

	return handlers.NewRouter(handlers.Deps{
		Config:          testServerConfig(),
		Logger:          slog.New(slog.DiscardHandler),
		Version:         "test",
		Tokens:          tokens,
		ProtectedRoutes: map[string]http.Handler{"/probe": probe},
	})
}

func get(router http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// A route mounted in the protected group must not be reachable without a token. This is
// the wiring test: the middleware working in isolation is not the same as it being
// applied.
func TestProtectedRouteRequiresAToken(t *testing.T) {
	var seen api.Identity
	reached := false
	router := protectedRouter(t, sessionTokens(t), &seen, &reached)

	rec := get(router, "/api/v1/probe", "")

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, reached, "the handler ran without authentication")
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), "Bearer")

	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.CodeUnauthorized, body.Error)
	require.NotEmpty(t, body.RequestID, "a rejection must still be traceable")
}

func TestProtectedRouteAcceptsAValidToken(t *testing.T) {
	tokens := sessionTokens(t)
	userID, teamID := uuid.New(), uuid.New()

	pair, err := tokens.IssuePair(userID, teamID)
	require.NoError(t, err)

	var seen api.Identity
	reached := false
	router := protectedRouter(t, tokens, &seen, &reached)

	require.Equal(t, http.StatusOK, get(router, "/api/v1/probe", pair.AccessToken).Code)
	require.True(t, reached)
	require.Equal(t, userID, seen.UserID)
	require.Equal(t, teamID, seen.TeamID)
}

// The auth endpoints are how a caller gets a token, so they must stay reachable without
// one. A protected group that swallowed them would lock everyone out.
func TestAuthEndpointsStayPublic(t *testing.T) {
	users, sessions := newFakeStore(), newFakeSessions()
	router := authRouterWithSessions(t, users, sessions)

	rec := postJSON(t, router, "/api/v1/auth/register", validRegistration)
	require.Equal(t, http.StatusCreated, rec.Code,
		"registration must not require the token it exists to issue")

	rec = postJSON(t, router, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"correct-horse-battery-staple"}`)
	require.Equal(t, http.StatusOK, rec.Code)
}

// Health is polled by orchestrators that have no credentials.
func TestHealthStaysPublic(t *testing.T) {
	var seen api.Identity
	reached := false
	router := protectedRouter(t, sessionTokens(t), &seen, &reached)

	require.Equal(t, http.StatusOK, get(router, "/health", "").Code)
}

// A token from a different deployment must not open a protected route.
func TestProtectedRouteRejectsAForeignToken(t *testing.T) {
	other, err := auth.NewTokens(config.AuthConfig{
		JWTSecret:       "ffffffffffffffffffffffffffffffff-other",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
	})
	require.NoError(t, err)

	pair, err := other.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	var seen api.Identity
	reached := false
	router := protectedRouter(t, sessionTokens(t), &seen, &reached)

	require.Equal(t, http.StatusUnauthorized,
		get(router, "/api/v1/probe", pair.AccessToken).Code)
	require.False(t, reached)
}
