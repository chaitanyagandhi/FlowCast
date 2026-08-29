package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/handlers"
	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
	"github.com/chaitanyagandhi/flowcast/backend/internal/repository"
)

// fakeUserStore is an in-memory UserStore, so the handlers can be exercised without a
// database while still enforcing the uniqueness rule the real schema does.
type fakeUserStore struct {
	byEmail map[string]models.User
	teams   map[uuid.UUID]models.Team

	createErr error
	findErr   error
	created   int
}

func newFakeStore() *fakeUserStore {
	return &fakeUserStore{
		byEmail: map[string]models.User{},
		teams:   map[uuid.UUID]models.Team{},
	}
}

func (f *fakeUserStore) CreateTeamWithOwner(_ context.Context, teamName string, user models.User) (models.Team, models.User, error) {
	if f.createErr != nil {
		return models.Team{}, models.User{}, f.createErr
	}
	key := repository.NormalizeEmail(user.Email)
	if _, taken := f.byEmail[key]; taken {
		return models.Team{}, models.User{}, models.ErrConflict
	}

	team := models.Team{ID: uuid.New(), Name: teamName, CreatedAt: time.Now().UTC()}
	user.ID = uuid.New()
	user.TeamID = team.ID
	user.Email = key
	user.CreatedAt = time.Now().UTC()

	f.teams[team.ID] = team
	f.byEmail[key] = user
	f.created++
	return team, user, nil
}

func (f *fakeUserStore) FindByEmail(_ context.Context, email string) (models.User, error) {
	if f.findErr != nil {
		return models.User{}, f.findErr
	}
	user, ok := f.byEmail[repository.NormalizeEmail(email)]
	if !ok {
		return models.User{}, models.ErrNotFound
	}
	return user, nil
}

func (f *fakeUserStore) FindByID(_ context.Context, id uuid.UUID) (models.User, error) {
	if f.findErr != nil {
		return models.User{}, f.findErr
	}
	for _, user := range f.byEmail {
		if user.ID == id {
			return user, nil
		}
	}
	return models.User{}, models.ErrNotFound
}

// fakeSessionStore is an in-memory revocation store. It ignores TTLs: the tests care
// about whether a token was withdrawn, not when Redis would forget it.
type fakeSessionStore struct {
	revoked map[uuid.UUID]time.Duration
	err     error
}

func newFakeSessions() *fakeSessionStore {
	return &fakeSessionStore{revoked: map[uuid.UUID]time.Duration{}}
}

func (f *fakeSessionStore) Revoke(_ context.Context, tokenID uuid.UUID, ttl time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.revoked[tokenID] = ttl
	return nil
}

func (f *fakeSessionStore) IsRevoked(_ context.Context, tokenID uuid.UUID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	_, found := f.revoked[tokenID]
	return found, nil
}

func authRouter(t *testing.T, store handlers.UserStore) http.Handler {
	t.Helper()
	return authRouterWithSessions(t, store, newFakeSessions())
}

func authRouterWithSessions(t *testing.T, store handlers.UserStore, sessions handlers.SessionStore) http.Handler {
	t.Helper()

	hasher, err := auth.NewHasher(bcrypt.MinCost)
	require.NoError(t, err)

	tokens, err := auth.NewTokens(config.AuthConfig{
		JWTSecret:       "0123456789abcdef0123456789abcdef-flowcast",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
	})
	require.NoError(t, err)

	return handlers.NewRouter(handlers.Deps{
		Config:   testServerConfig(),
		Logger:   slog.New(slog.DiscardHandler),
		Version:  "test",
		Users:    store,
		Sessions: sessions,
		Hasher:   hasher,
		Tokens:   tokens,
	})
}

func postJSON(t *testing.T, router http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type sessionBody struct {
	User struct {
		ID     string `json:"id"`
		TeamID string `json:"team_id"`
		Email  string `json:"email"`
		Name   string `json:"name"`
	} `json:"user"`
	Team struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func decodeSession(t *testing.T, rec *httptest.ResponseRecorder) sessionBody {
	t.Helper()
	var body sessionBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	return body
}

func refreshCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == handlers.RefreshCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie was set", handlers.RefreshCookieName)
	return nil
}

const validRegistration = `{
	"email":"ada@example.com",
	"password":"correct-horse-battery-staple",
	"name":"Ada Lovelace",
	"team_name":"Platform"
}`

// --- Registration ---

func TestRegisterCreatesTeamAndUser(t *testing.T) {
	store := newFakeStore()
	rec := postJSON(t, authRouter(t, store), "/api/v1/auth/register", validRegistration)

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	body := decodeSession(t, rec)
	require.Equal(t, "ada@example.com", body.User.Email)
	require.Equal(t, "Ada Lovelace", body.User.Name)
	require.Equal(t, "Platform", body.Team.Name)
	require.Equal(t, body.Team.ID, body.User.TeamID, "the user must belong to the new team")
	require.NotEmpty(t, body.AccessToken)
	require.Equal(t, 1, store.created)
}

// The password must not come back in any form, hashed or otherwise.
func TestRegisterNeverEchoesTheCredential(t *testing.T) {
	rec := postJSON(t, authRouter(t, newFakeStore()), "/api/v1/auth/register", validRegistration)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotContains(t, rec.Body.String(), "correct-horse-battery-staple")
	require.NotContains(t, rec.Body.String(), "password")
	require.NotContains(t, rec.Body.String(), "$2a$")
}

// The long-lived credential belongs in an HttpOnly cookie, not the response body, so the
// frontend can keep nothing in localStorage.
func TestRegisterSetsHardenedRefreshCookie(t *testing.T) {
	rec := postJSON(t, authRouter(t, newFakeStore()), "/api/v1/auth/register", validRegistration)

	cookie := refreshCookie(t, rec)
	require.NotEmpty(t, cookie.Value)
	require.True(t, cookie.HttpOnly, "script must not be able to read the refresh token")
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	require.Equal(t, handlers.RefreshCookiePath, cookie.Path,
		"the cookie should not be attached to every API call")
	require.True(t, cookie.Expires.After(time.Now()))

	// The refresh token must not also appear in the body.
	require.NotContains(t, rec.Body.String(), cookie.Value)
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	router := authRouter(t, newFakeStore())

	require.Equal(t, http.StatusCreated,
		postJSON(t, router, "/api/v1/auth/register", validRegistration).Code)

	rec := postJSON(t, router, "/api/v1/auth/register", validRegistration)
	require.Equal(t, http.StatusConflict, rec.Code)

	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.CodeConflict, body.Error)
}

// Registration is case-insensitive on email, matching the unique index in the schema.
func TestRegisterTreatsEmailCaseInsensitively(t *testing.T) {
	router := authRouter(t, newFakeStore())

	require.Equal(t, http.StatusCreated,
		postJSON(t, router, "/api/v1/auth/register", validRegistration).Code)

	shouted := strings.Replace(validRegistration, "ada@example.com", "ADA@Example.COM", 1)
	require.Equal(t, http.StatusConflict,
		postJSON(t, router, "/api/v1/auth/register", shouted).Code)
}

func TestRegisterReportsEveryInvalidFieldAtOnce(t *testing.T) {
	rec := postJSON(t, authRouter(t, newFakeStore()), "/api/v1/auth/register",
		`{"email":"not-an-email","password":"short","name":"","team_name":""}`)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.CodeValidationFailed, body.Error)

	fields := map[string]bool{}
	for _, issue := range body.Fields {
		fields[issue.Field] = true
	}
	for _, want := range []string{"email", "password", "name", "team_name"} {
		require.True(t, fields[want], "expected an issue for %q, got %v", want, body.Fields)
	}
}

func TestRegisterRejectsMalformedRequests(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantStatus int
	}{
		"not json":        {`{`, http.StatusBadRequest},
		"empty body":      {``, http.StatusBadRequest},
		"wrong type":      {`{"email":123}`, http.StatusBadRequest},
		"unknown field":   {`{"emial":"a@b.com"}`, http.StatusBadRequest},
		"two documents":   {`{"email":"a@b.com"}{"email":"c@d.com"}`, http.StatusBadRequest},
		"empty json body": {`{}`, http.StatusUnprocessableEntity},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := postJSON(t, authRouter(t, newFakeStore()), "/api/v1/auth/register", tc.body)
			require.Equal(t, tc.wantStatus, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// A stale client sending a renamed field should be told, not silently registered with a
// missing value.
func TestRegisterNamesTheUnknownField(t *testing.T) {
	rec := postJSON(t, authRouter(t, newFakeStore()), "/api/v1/auth/register",
		`{"email":"a@b.com","password":"correct-horse-battery-staple","name":"A","team_name":"T","admin":true}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "admin")
}

func TestRegisterRequiresJSONContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(validRegistration))
	req.Header.Set("Content-Type", "text/plain")

	rec := httptest.NewRecorder()
	authRouter(t, newFakeStore()).ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "application/json")
}

func TestRegisterReportsStorageFailureAsInternal(t *testing.T) {
	store := newFakeStore()
	store.createErr = errors.New(`pq: connection to host=prod-db.internal failed`)

	rec := postJSON(t, authRouter(t, store), "/api/v1/auth/register", validRegistration)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "prod-db.internal")
}

// --- Login ---

func registerThenLogin(t *testing.T, body string) (*httptest.ResponseRecorder, http.Handler) {
	t.Helper()
	router := authRouter(t, newFakeStore())
	require.Equal(t, http.StatusCreated,
		postJSON(t, router, "/api/v1/auth/register", validRegistration).Code)
	return postJSON(t, router, "/api/v1/auth/login", body), router
}

func TestLoginSucceedsWithCorrectCredentials(t *testing.T) {
	rec, _ := registerThenLogin(t,
		`{"email":"ada@example.com","password":"correct-horse-battery-staple"}`)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	body := decodeSession(t, rec)
	require.Equal(t, "ada@example.com", body.User.Email)
	require.NotEmpty(t, body.AccessToken)
	require.True(t, body.ExpiresAt.After(time.Now()))

	cookie := refreshCookie(t, rec)
	require.True(t, cookie.HttpOnly)
}

func TestLoginAcceptsAnyCasingOfTheEmail(t *testing.T) {
	rec, _ := registerThenLogin(t,
		`{"email":"ADA@Example.COM","password":"correct-horse-battery-staple"}`)
	require.Equal(t, http.StatusOK, rec.Code)
}

// The wrong-password and unknown-account responses must be indistinguishable, or the
// endpoint becomes a way to discover who has an account.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	wrongPassword, router := registerThenLogin(t,
		`{"email":"ada@example.com","password":"not-the-right-password"}`)

	unknownUser := postJSON(t, router, "/api/v1/auth/login",
		`{"email":"nobody@example.com","password":"not-the-right-password"}`)

	require.Equal(t, http.StatusUnauthorized, wrongPassword.Code)
	require.Equal(t, http.StatusUnauthorized, unknownUser.Code)

	var a, b api.ErrorResponse
	require.NoError(t, json.Unmarshal(wrongPassword.Body.Bytes(), &a))
	require.NoError(t, json.Unmarshal(unknownUser.Body.Bytes(), &b))

	require.Equal(t, a.Error, b.Error)
	require.Equal(t, a.Message, b.Message,
		"the two failures must not be tellable apart from the response")
}

func TestLoginSetsNoCookieOnFailure(t *testing.T) {
	rec, _ := registerThenLogin(t, `{"email":"ada@example.com","password":"wrong-password-x"}`)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	for _, c := range rec.Result().Cookies() {
		require.NotEqual(t, handlers.RefreshCookieName, c.Name,
			"a failed login must not hand out a session")
	}
}

func TestLoginRejectsMalformedRequests(t *testing.T) {
	router := authRouter(t, newFakeStore())

	for name, body := range map[string]string{
		"not json":      `{`,
		"unknown field": `{"email":"a@b.com","password":"x","remember":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, http.StatusBadRequest,
				postJSON(t, router, "/api/v1/auth/login", body).Code)
		})
	}
}

// An empty login is a failed login, not a validation report: saying which field was
// missing would confirm whether the address exists.
func TestLoginWithEmptyCredentialsIsUnauthorized(t *testing.T) {
	rec := postJSON(t, authRouter(t, newFakeStore()), "/api/v1/auth/login", `{}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLoginReportsStorageFailureAsInternal(t *testing.T) {
	store := newFakeStore()
	store.findErr = errors.New("connection refused to prod-db.internal")

	rec := postJSON(t, authRouter(t, store), "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"correct-horse-battery-staple"}`)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "prod-db.internal")
}

// --- Token contents ---

// The issued access token must carry the identity the rest of the system will trust.
func TestIssuedAccessTokenCarriesUserAndTeam(t *testing.T) {
	store := newFakeStore()
	rec := postJSON(t, authRouter(t, store), "/api/v1/auth/register", validRegistration)
	require.Equal(t, http.StatusCreated, rec.Code)

	body := decodeSession(t, rec)

	tokens, err := auth.NewTokens(config.AuthConfig{
		JWTSecret:       "0123456789abcdef0123456789abcdef-flowcast",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
	})
	require.NoError(t, err)

	claims, err := tokens.ParseAccess(body.AccessToken)
	require.NoError(t, err)

	userID, err := claims.UserID()
	require.NoError(t, err)
	require.Equal(t, body.User.ID, userID.String())
	require.Equal(t, body.User.TeamID, claims.TeamID.String())
}

// The cookie must hold a refresh token, not a second access token.
func TestRefreshCookieHoldsARefreshToken(t *testing.T) {
	rec := postJSON(t, authRouter(t, newFakeStore()), "/api/v1/auth/register", validRegistration)
	cookie := refreshCookie(t, rec)

	tokens, err := auth.NewTokens(config.AuthConfig{
		JWTSecret:       "0123456789abcdef0123456789abcdef-flowcast",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
	})
	require.NoError(t, err)

	claims, err := tokens.ParseRefresh(cookie.Value)
	require.NoError(t, err)
	require.Equal(t, auth.TokenRefresh, claims.Kind)

	_, err = tokens.ParseAccess(cookie.Value)
	require.ErrorIs(t, err, auth.ErrWrongTokenKind)
}

func TestAuthRoutesRejectWrongMethod(t *testing.T) {
	router := authRouter(t, newFakeStore())

	for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "GET %s", path)
	}
}
