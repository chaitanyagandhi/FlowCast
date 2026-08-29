package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/middleware"
)

const testJWTSecret = "0123456789abcdef0123456789abcdef-flowcast"

func testAuthConfig() config.AuthConfig {
	return config.AuthConfig{
		JWTSecret:       testJWTSecret,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
	}
}

func testTokens(t *testing.T, opts ...auth.Option) *auth.Tokens {
	t.Helper()
	tokens, err := auth.NewTokens(testAuthConfig(), opts...)
	require.NoError(t, err)
	return tokens
}

// guarded wraps a handler that records the identity the middleware attached.
func guarded(t *testing.T, tokens *auth.Tokens) (http.Handler, *api.Identity, *bool) {
	t.Helper()

	var seen api.Identity
	reached := false

	logger, _ := captureLogs()
	handler := middleware.Authenticate(tokens, logger)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			identity, ok := api.IdentityFrom(r.Context())
			require.True(t, ok, "an authenticated request must carry an identity")
			seen = identity
			w.WriteHeader(http.StatusOK)
		}))

	return handler, &seen, &reached
}

func withBearer(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) api.ErrorResponse {
	t.Helper()
	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	return body
}

func TestValidTokenAttachesIdentity(t *testing.T) {
	tokens := testTokens(t)
	userID, teamID := uuid.New(), uuid.New()

	pair, err := tokens.IssuePair(userID, teamID)
	require.NoError(t, err)

	handler, seen, reached := guarded(t, tokens)
	rec := serve(handler, withBearer(pair.AccessToken))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, *reached)
	require.Equal(t, userID, seen.UserID)
	require.Equal(t, teamID, seen.TeamID, "the tenant boundary comes from the token")
}

// The handler must never run for an unauthenticated request: a handler that assumes it is
// authenticated would otherwise query with a zero team id.
func TestRejectedRequestNeverReachesTheHandler(t *testing.T) {
	tokens := testTokens(t)

	handler, _, reached := guarded(t, tokens)
	rec := serve(handler, withBearer(""))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, *reached, "the handler ran without an authenticated caller")
}

func TestMalformedAuthorizationHeadersAreRejected(t *testing.T) {
	tokens := testTokens(t)
	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	headers := map[string]string{
		"empty":              "",
		"no scheme":          pair.AccessToken,
		"wrong scheme":       "Basic " + pair.AccessToken,
		"scheme only":        "Bearer",
		"scheme with spaces": "Bearer    ",
		"token then junk":    "Token " + pair.AccessToken,
	}

	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			handler, _, reached := guarded(t, tokens)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}

			rec := serve(handler, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.False(t, *reached)
		})
	}
}

// RFC 7235 treats the scheme as case-insensitive, and real clients vary.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	tokens := testTokens(t)
	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "BeArEr"} {
		t.Run(scheme, func(t *testing.T) {
			handler, _, reached := guarded(t, tokens)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
			req.Header.Set("Authorization", scheme+" "+pair.AccessToken)

			require.Equal(t, http.StatusOK, serve(handler, req).Code)
			require.True(t, *reached)
		})
	}
}

// An expired token gets its own code so the client knows to refresh rather than to send
// the user back to the sign-in page.
func TestExpiredTokenIsReportedDistinctly(t *testing.T) {
	now := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tokens := testTokens(t, auth.WithClock(clock))

	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	now = now.Add(16 * time.Minute)

	handler, _, reached := guarded(t, tokens)
	rec := serve(handler, withBearer(pair.AccessToken))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, *reached)
	require.Equal(t, api.CodeTokenExpired, errorBody(t, rec).Error)
}

// A refresh token presented as a bearer credential must not authenticate a request.
func TestRefreshTokenCannotAuthenticateARequest(t *testing.T) {
	tokens := testTokens(t)
	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	handler, _, reached := guarded(t, tokens)
	rec := serve(handler, withBearer(pair.RefreshToken))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, *reached)
	require.Equal(t, api.CodeUnauthorized, errorBody(t, rec).Error,
		"a wrong-kind token is not an expiry problem")
}

func TestTokenFromAnotherDeploymentIsRejected(t *testing.T) {
	tokens := testTokens(t)

	otherCfg := testAuthConfig()
	otherCfg.JWTSecret = "ffffffffffffffffffffffffffffffff-other"
	other, err := auth.NewTokens(otherCfg)
	require.NoError(t, err)

	pair, err := other.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	handler, _, reached := guarded(t, tokens)
	require.Equal(t, http.StatusUnauthorized, serve(handler, withBearer(pair.AccessToken)).Code)
	require.False(t, *reached)
}

// A forged token claiming another tenant must not get through. This is the attack the
// whole design exists to stop.
func TestForgedTeamClaimIsRejected(t *testing.T) {
	tokens := testTokens(t)

	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":     uuid.New().String(),
		"team_id": uuid.New().String(),
		"typ":     "access",
		"iss":     "flowcast",
		"aud":     "flowcast-api",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("an-attacker-controlled-signing-key"))
	require.NoError(t, err)

	handler, _, reached := guarded(t, tokens)
	require.Equal(t, http.StatusUnauthorized, serve(handler, withBearer(forged)).Code)
	require.False(t, *reached, "an unverifiable team claim must never reach a handler")
}

// A 401 has to carry a challenge, per RFC 7235.
func TestRejectionCarriesAChallengeAndJSONBody(t *testing.T) {
	tokens := testTokens(t)

	handler, _, _ := guarded(t, tokens)
	rec := serve(handler, withBearer("not-a-token"))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), "Bearer")
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, api.CodeUnauthorized, errorBody(t, rec).Error)
}

// Why a request failed is an operator's business, not a caller's.
func TestRejectionReasonIsLoggedNotReturned(t *testing.T) {
	tokens := testTokens(t)
	logger, buf := captureLogs()

	handler := middleware.Authenticate(tokens, logger)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	rec := serve(handler, withBearer("garbage.token.value"))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, buf.String(), "request not authenticated")
	require.Contains(t, buf.String(), "invalid access token")

	// The response says only that authentication is needed.
	require.NotContains(t, rec.Body.String(), "invalid access token")
	require.NotContains(t, rec.Body.String(), "signature")
}

// Two callers on one server must not see each other's identity.
func TestIdentityDoesNotLeakBetweenRequests(t *testing.T) {
	tokens := testTokens(t)

	firstUser, firstTeam := uuid.New(), uuid.New()
	secondUser, secondTeam := uuid.New(), uuid.New()

	firstPair, err := tokens.IssuePair(firstUser, firstTeam)
	require.NoError(t, err)
	secondPair, err := tokens.IssuePair(secondUser, secondTeam)
	require.NoError(t, err)

	logger, _ := captureLogs()

	var seen []api.Identity
	handler := middleware.Authenticate(tokens, logger)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := api.IdentityFrom(r.Context())
			require.True(t, ok)
			seen = append(seen, identity)
			w.WriteHeader(http.StatusOK)
		}))

	serve(handler, withBearer(firstPair.AccessToken))
	serve(handler, withBearer(secondPair.AccessToken))
	serve(handler, withBearer(firstPair.AccessToken))

	require.Equal(t, []api.Identity{
		{UserID: firstUser, TeamID: firstTeam},
		{UserID: secondUser, TeamID: secondTeam},
		{UserID: firstUser, TeamID: firstTeam},
	}, seen)
}

// Outside the middleware there is no identity, and that has to be detectable rather than
// silently reading as a zero team.
func TestIdentityIsAbsentWithoutTheMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)

	identity, ok := api.IdentityFrom(req.Context())
	require.False(t, ok)
	require.Equal(t, uuid.Nil, identity.TeamID)
}
