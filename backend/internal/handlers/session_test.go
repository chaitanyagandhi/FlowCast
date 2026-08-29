package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/handlers"
)

// session is a registered account plus the cookie its registration handed back.
type session struct {
	router  http.Handler
	cookie  *http.Cookie
	users   *fakeUserStore
	revokes *fakeSessionStore
}

func newSession(t *testing.T) session {
	t.Helper()
	users, sessions := newFakeStore(), newFakeSessions()
	router := authRouterWithSessions(t, users, sessions)

	rec := postJSON(t, router, "/api/v1/auth/register", validRegistration)
	require.Equal(t, http.StatusCreated, rec.Code)

	return session{router: router, cookie: refreshCookie(t, rec), users: users, revokes: sessions}
}

// post sends a bodyless request, optionally carrying a refresh cookie.
func post(t *testing.T, router http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func clearedCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == handlers.RefreshCookieName {
			return c
		}
	}
	t.Fatal("expected the refresh cookie to be addressed")
	return nil
}

// --- Refresh ---

func TestRefreshIssuesANewAccessToken(t *testing.T) {
	s := newSession(t)

	rec := post(t, s.router, "/api/v1/auth/refresh", s.cookie)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	body := decodeSession(t, rec)
	require.NotEmpty(t, body.AccessToken)
	require.True(t, body.ExpiresAt.After(time.Now()))
	require.Equal(t, "ada@example.com", body.User.Email)
}

// Rotation is the point: the presented token is spent, so a leaked one is useful only
// until the real client next refreshes.
func TestRefreshRotatesTheRefreshToken(t *testing.T) {
	s := newSession(t)

	rec := post(t, s.router, "/api/v1/auth/refresh", s.cookie)
	require.Equal(t, http.StatusOK, rec.Code)

	rotated := refreshCookie(t, rec)
	require.NotEqual(t, s.cookie.Value, rotated.Value, "a new refresh token must be issued")
	require.True(t, rotated.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, rotated.SameSite)
	require.Len(t, s.revokes.revoked, 1, "the spent token must be revoked")
}

func TestRotatedRefreshTokenStopsWorking(t *testing.T) {
	s := newSession(t)

	first := post(t, s.router, "/api/v1/auth/refresh", s.cookie)
	require.Equal(t, http.StatusOK, first.Code)

	// Replaying the original cookie must fail now that it has been spent.
	replay := post(t, s.router, "/api/v1/auth/refresh", s.cookie)
	require.Equal(t, http.StatusUnauthorized, replay.Code)

	// The replacement still works.
	next := post(t, s.router, "/api/v1/auth/refresh", refreshCookie(t, first))
	require.Equal(t, http.StatusOK, next.Code)
}

func TestRefreshWithoutACookieIsUnauthorized(t *testing.T) {
	s := newSession(t)

	rec := post(t, s.router, "/api/v1/auth/refresh", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.CodeUnauthorized, body.Error)
}

// An access token in the refresh cookie must not be accepted: the kinds are separate for
// a reason, and this is the path that would otherwise let one be swapped for the other.
func TestRefreshRejectsAnAccessToken(t *testing.T) {
	users, sessions := newFakeStore(), newFakeSessions()
	router := authRouterWithSessions(t, users, sessions)

	rec := postJSON(t, router, "/api/v1/auth/register", validRegistration)
	require.Equal(t, http.StatusCreated, rec.Code)
	accessToken := decodeSession(t, rec).AccessToken

	forged := &http.Cookie{Name: handlers.RefreshCookieName, Value: accessToken}
	require.Equal(t, http.StatusUnauthorized,
		post(t, router, "/api/v1/auth/refresh", forged).Code)
}

func TestRefreshRejectsAGarbageCookie(t *testing.T) {
	s := newSession(t)

	for name, value := range map[string]string{
		"not a token": "hello",
		"empty":       "",
		"tampered":    s.cookie.Value[:len(s.cookie.Value)-4] + "AAAA",
	} {
		t.Run(name, func(t *testing.T) {
			bad := &http.Cookie{Name: handlers.RefreshCookieName, Value: value}
			require.Equal(t, http.StatusUnauthorized,
				post(t, s.router, "/api/v1/auth/refresh", bad).Code)
		})
	}
}

// A rejected session should also stop the browser resending the dead cookie.
func TestRefreshClearsTheCookieOnRejection(t *testing.T) {
	s := newSession(t)

	bad := &http.Cookie{Name: handlers.RefreshCookieName, Value: "not-a-token"}
	rec := post(t, s.router, "/api/v1/auth/refresh", bad)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	cleared := clearedCookie(t, rec)
	require.Empty(t, cleared.Value)
	require.Equal(t, -1, cleared.MaxAge)
	require.Equal(t, handlers.RefreshCookiePath, cleared.Path,
		"attributes must match the original or the browser keeps it")
}

// A refresh token can outlive the account it names, so the user is re-read rather than
// trusted from the claims.
func TestRefreshFailsWhenTheUserIsGone(t *testing.T) {
	s := newSession(t)

	// The account is removed while the cookie is still valid and unrevoked.
	clear(s.users.byEmail)

	rec := post(t, s.router, "/api/v1/auth/refresh", s.cookie)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Empty(t, clearedCookie(t, rec).Value)
}

// Redis being unreachable must not quietly re-enable every logged-out token.
func TestRefreshFailsClosedWhenRevocationStoreIsDown(t *testing.T) {
	s := newSession(t)
	s.revokes.err = errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")

	rec := post(t, s.router, "/api/v1/auth/refresh", s.cookie)

	require.Equal(t, http.StatusInternalServerError, rec.Code,
		"an unreachable revocation store must not be read as 'not revoked'")
	require.NotContains(t, rec.Body.String(), "6379")
}

// --- Logout ---

func TestLogoutRevokesTheSessionAndClearsTheCookie(t *testing.T) {
	s := newSession(t)

	rec := post(t, s.router, "/api/v1/auth/logout", s.cookie)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.String())

	cleared := clearedCookie(t, rec)
	require.Empty(t, cleared.Value)
	require.Equal(t, -1, cleared.MaxAge)
	require.True(t, cleared.HttpOnly)

	require.Len(t, s.revokes.revoked, 1)
}

func TestRefreshAfterLogoutIsRejected(t *testing.T) {
	s := newSession(t)

	require.Equal(t, http.StatusNoContent,
		post(t, s.router, "/api/v1/auth/logout", s.cookie).Code)

	require.Equal(t, http.StatusUnauthorized,
		post(t, s.router, "/api/v1/auth/refresh", s.cookie).Code)
}

// Logout always succeeds. Reporting whether the token was valid tells someone holding a
// stale cookie something they cannot otherwise learn.
func TestLogoutSucceedsWithoutOrWithABadCookie(t *testing.T) {
	s := newSession(t)

	require.Equal(t, http.StatusNoContent,
		post(t, s.router, "/api/v1/auth/logout", nil).Code)

	bad := &http.Cookie{Name: handlers.RefreshCookieName, Value: "not-a-token"}
	require.Equal(t, http.StatusNoContent,
		post(t, s.router, "/api/v1/auth/logout", bad).Code)

	require.Empty(t, s.revokes.revoked, "nothing to revoke for a token that was never valid")
}

func TestLogoutIsIdempotent(t *testing.T) {
	s := newSession(t)

	for range 3 {
		require.Equal(t, http.StatusNoContent,
			post(t, s.router, "/api/v1/auth/logout", s.cookie).Code)
	}
}

// The browser forgets the session either way, but a revocation that did not stick is an
// operator problem worth surfacing.
func TestLogoutReportsAFailedRevocation(t *testing.T) {
	s := newSession(t)
	s.revokes.err = errors.New("connection refused to redis-prod.internal")

	rec := post(t, s.router, "/api/v1/auth/logout", s.cookie)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "redis-prod.internal")
	require.Empty(t, clearedCookie(t, rec).Value, "the cookie is still cleared")
}

// --- Revocation bookkeeping ---

// A revocation entry only has to outlive the token it cancels.
func TestRevocationTTLMatchesTheRemainingTokenLife(t *testing.T) {
	s := newSession(t)

	require.Equal(t, http.StatusNoContent,
		post(t, s.router, "/api/v1/auth/logout", s.cookie).Code)

	require.Len(t, s.revokes.revoked, 1)
	for _, ttl := range s.revokes.revoked {
		require.Positive(t, ttl)
		require.LessOrEqual(t, ttl, 720*time.Hour,
			"the entry must not outlive the refresh token itself")
	}
}

func TestSessionRoutesRejectWrongMethod(t *testing.T) {
	s := newSession(t)

	for _, path := range []string{"/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "GET %s", path)
	}
}
