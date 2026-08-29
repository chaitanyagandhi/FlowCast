package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
	"github.com/chaitanyagandhi/flowcast/backend/internal/repository"
)

// RefreshCookieName carries the refresh token.
//
// The refresh token lives in an HttpOnly cookie rather than in the response body, and the
// access token is returned in the body for the client to hold in memory. That split is
// what lets the frontend keep nothing in localStorage: script can never read the
// long-lived credential, and the short-lived one disappears when the tab closes.
const RefreshCookieName = "flowcast_refresh"

// RefreshCookiePath scopes the cookie to the auth endpoints, so it is not attached to
// every API call that has no use for it.
const RefreshCookiePath = "/api/v1/auth"

// UserStore is the persistence the auth handlers need. An interface, so the handlers can
// be tested without a database.
type UserStore interface {
	CreateTeamWithOwner(ctx context.Context, teamName string, user models.User) (models.Team, models.User, error)
	FindByEmail(ctx context.Context, email string) (models.User, error)
}

// AuthHandler serves registration and login.
type AuthHandler struct {
	users  UserStore
	hasher *auth.Hasher
	tokens *auth.Tokens
	logger *slog.Logger
	// secureCookies marks the refresh cookie Secure. Off outside production because a
	// Secure cookie is not sent over plain http, which would break local development.
	secureCookies bool
}

// NewAuthHandler builds the registration and login handlers.
func NewAuthHandler(users UserStore, hasher *auth.Hasher, tokens *auth.Tokens,
	secureCookies bool, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{
		users: users, hasher: hasher, tokens: tokens,
		secureCookies: secureCookies, logger: logger,
	}
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	TeamName string `json:"team_name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// authResponse is what both endpoints return. The refresh token is absent by design: it
// travels only in the HttpOnly cookie.
type authResponse struct {
	User        models.User `json:"user"`
	Team        models.Team `json:"team"`
	AccessToken string      `json:"access_token"`
	ExpiresAt   time.Time   `json:"expires_at"`
}

// Register creates a team and its first user.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := api.DecodeJSON(w, r, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, api.CodeBadRequest, err.Error())
		return
	}

	if err := validateRegistration(req); err != nil {
		api.WriteValidationError(w, r, err)
		return
	}

	hash, err := h.hasher.Hash(req.Password)
	if err != nil {
		api.WriteInternalError(w, r, err)
		return
	}

	team, user, err := h.users.CreateTeamWithOwner(r.Context(), req.TeamName, models.User{
		Email:        req.Email,
		PasswordHash: hash,
		Name:         req.Name,
	})
	if err != nil {
		if errors.Is(err, models.ErrConflict) {
			api.WriteError(w, r, http.StatusConflict, api.CodeConflict,
				"An account with that email address already exists.")
			return
		}
		api.WriteInternalError(w, r, err)
		return
	}

	h.logger.InfoContext(r.Context(), "user registered",
		"user_id", user.ID, "team_id", team.ID, "request_id", api.RequestID(r.Context()))

	h.respondWithSession(w, r, http.StatusCreated, user, team)
}

// Login exchanges credentials for a session.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := api.DecodeJSON(w, r, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, api.CodeBadRequest, err.Error())
		return
	}

	user, err := h.users.FindByEmail(r.Context(), req.Email)
	switch {
	case errors.Is(err, models.ErrNotFound):
		// Burn the same work a real verification would, so response time does not
		// reveal which addresses are registered, then fail identically.
		_ = h.hasher.VerifyDummy()
		h.rejectCredentials(w, r, req.Email, "no such user")
		return

	case err != nil:
		api.WriteInternalError(w, r, err)
		return
	}

	if err := h.hasher.Verify(user.PasswordHash, req.Password); err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			// A stored hash that will not parse is an operator problem, not a user one.
			h.logger.ErrorContext(r.Context(), "stored password hash is unusable",
				"user_id", user.ID, "error", err)
		}
		h.rejectCredentials(w, r, req.Email, "password mismatch")
		return
	}

	h.logger.InfoContext(r.Context(), "user logged in",
		"user_id", user.ID, "team_id", user.TeamID, "request_id", api.RequestID(r.Context()))

	// The team is not loaded on login: the token carries team_id, and the dashboard
	// fetches whatever else it needs.
	h.respondWithSession(w, r, http.StatusOK, user, models.Team{ID: user.TeamID})
}

// rejectCredentials answers every failed login identically.
//
// The reason is logged but never returned. Distinguishing "no such account" from "wrong
// password" turns the login endpoint into a way to discover who has an account here.
func (h *AuthHandler) rejectCredentials(w http.ResponseWriter, r *http.Request, email, reason string) {
	h.logger.WarnContext(r.Context(), "login rejected",
		"reason", reason,
		"email", repository.NormalizeEmail(email),
		"request_id", api.RequestID(r.Context()))

	api.WriteError(w, r, http.StatusUnauthorized, api.CodeUnauthorized,
		"Invalid email or password.")
}

// respondWithSession issues tokens, sets the refresh cookie, and writes the response.
func (h *AuthHandler) respondWithSession(w http.ResponseWriter, r *http.Request,
	status int, user models.User, team models.Team) {
	pair, err := h.tokens.IssuePair(user.ID, user.TeamID)
	if err != nil {
		api.WriteInternalError(w, r, err)
		return
	}

	h.setRefreshCookie(w, pair)

	api.WriteJSON(w, r, status, authResponse{
		User:        user,
		Team:        team,
		AccessToken: pair.AccessToken,
		ExpiresAt:   pair.AccessExpiresAt,
	})
}

func (h *AuthHandler) setRefreshCookie(w http.ResponseWriter, pair auth.TokenPair) {
	http.SetCookie(w, &http.Cookie{
		Name:  RefreshCookieName,
		Value: pair.RefreshToken,
		Path:  RefreshCookiePath,
		// Script must never be able to read the long-lived credential.
		HttpOnly: true,
		Secure:   h.secureCookies,
		// Strict: the refresh call is always a same-site request from our own frontend,
		// so there is no reason to attach this cookie to a cross-site navigation. A
		// deployment that genuinely splits frontend and API across different sites would
		// need SameSite=None with Secure.
		SameSite: http.SameSiteStrictMode,
		Expires:  pair.RefreshExpiresAt,
	})
}

// validateRegistration checks a registration payload, reporting every problem at once so
// a form can be corrected in one round trip.
//
// The fields are checked here rather than by building a models.User first, because the
// request has its own shape: team_name and name are two different fields that a shared
// validator would both report as "name".
func validateRegistration(req registerRequest) error {
	var issues []models.FieldIssue
	add := func(field, message string) {
		issues = append(issues, models.FieldIssue{Field: field, Message: message})
	}

	email := strings.TrimSpace(req.Email)
	switch {
	case email == "":
		add("email", "is required")
	case len(email) > maxEmailLength:
		add("email", "is too long")
	case !models.ValidEmail(email):
		add("email", "must be a valid email address")
	}

	if strings.TrimSpace(req.Name) == "" {
		add("name", "is required")
	}
	if strings.TrimSpace(req.TeamName) == "" {
		add("team_name", "is required")
	}

	// Password policy lives in the auth package; surface its issues unchanged.
	var passwordErr *models.ValidationError
	if errors.As(auth.ValidatePassword(req.Password), &passwordErr) {
		issues = append(issues, passwordErr.Issues...)
	}

	if len(issues) == 0 {
		return nil
	}
	return &models.ValidationError{Issues: issues}
}

// maxEmailLength matches the CHECK constraint on users.email.
const maxEmailLength = 320
