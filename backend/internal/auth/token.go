package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

// Issuer and audience are fixed strings, verified on every parse. They stop a token minted
// for some other system that happens to share a signing key from being accepted here.
const (
	tokenIssuer   = "flowcast"
	tokenAudience = "flowcast-api"
)

// signingMethod is the only algorithm accepted, asserted on parse rather than read from
// the token. Trusting a token's own alg header is how "none" and RS256-to-HS256 confusion
// attacks work: the attacker picks the algorithm, and the verifier obliges.
var signingMethod = jwt.SigningMethodHS256

// TokenType separates the two kinds of token. A refresh token is long-lived and must never
// be accepted where an access token is expected, so the type is a signed claim rather than
// an assumption about which endpoint received it.
type TokenType string

const (
	TokenAccess  TokenType = "access"
	TokenRefresh TokenType = "refresh"
)

// Token errors. Callers should map all of them to the same HTTP response; the distinction
// exists for logs and for deciding whether a refresh is worth attempting.
var (
	ErrTokenInvalid   = errors.New("token invalid")
	ErrTokenExpired   = errors.New("token expired")
	ErrWrongTokenKind = errors.New("token is of the wrong kind")
)

// Claims is FlowCast's JWT payload.
//
// TeamID travels in the token so tenant scoping needs no extra lookup, and so a request
// cannot claim a team the token was not issued for.
type Claims struct {
	jwt.RegisteredClaims
	TeamID uuid.UUID `json:"team_id"`
	Kind   TokenType `json:"typ"`
}

// UserID returns the subject as a UUID.
func (c *Claims) UserID() (uuid.UUID, error) {
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("token subject is not a uuid: %w", err)
	}
	return id, nil
}

// TokenPair is what a successful login or refresh hands back.
type TokenPair struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	// RefreshID is the refresh token's jti. Recording it is what makes a specific
	// refresh token revocable at logout.
	RefreshID uuid.UUID
}

// Tokens issues and verifies FlowCast's JWTs.
type Tokens struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

// Option customises a Tokens instance.
type Option func(*Tokens)

// WithClock replaces the time source, so expiry can be tested without sleeping.
func WithClock(now func() time.Time) Option {
	return func(t *Tokens) { t.now = now }
}

// NewTokens builds a token issuer from auth configuration.
func NewTokens(cfg config.AuthConfig, opts ...Option) (*Tokens, error) {
	if len(cfg.JWTSecret) < minSecretLength {
		return nil, fmt.Errorf("jwt secret must be at least %d bytes", minSecretLength)
	}
	if cfg.AccessTokenTTL <= 0 || cfg.RefreshTokenTTL <= 0 {
		return nil, errors.New("token lifetimes must be positive")
	}
	if cfg.AccessTokenTTL >= cfg.RefreshTokenTTL {
		return nil, errors.New("access token must be shorter lived than the refresh token")
	}

	t := &Tokens{
		secret:     []byte(cfg.JWTSecret),
		accessTTL:  cfg.AccessTokenTTL,
		refreshTTL: cfg.RefreshTokenTTL,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

// minSecretLength mirrors the configuration floor, repeated so a hand-built AuthConfig
// cannot bypass it.
const minSecretLength = 32

// IssuePair mints a fresh access and refresh token for a user.
func (t *Tokens) IssuePair(userID, teamID uuid.UUID) (TokenPair, error) {
	if userID == uuid.Nil || teamID == uuid.Nil {
		return TokenPair{}, errors.New("user and team ids are required to issue a token")
	}

	issuedAt := t.now()
	accessExpiry := issuedAt.Add(t.accessTTL)
	refreshExpiry := issuedAt.Add(t.refreshTTL)
	refreshID := uuid.New()

	access, err := t.sign(newClaims(userID, teamID, TokenAccess, uuid.New(), issuedAt, accessExpiry))
	if err != nil {
		return TokenPair{}, err
	}
	refresh, err := t.sign(newClaims(userID, teamID, TokenRefresh, refreshID, issuedAt, refreshExpiry))
	if err != nil {
		return TokenPair{}, err
	}

	return TokenPair{
		AccessToken:      access,
		RefreshToken:     refresh,
		AccessExpiresAt:  accessExpiry,
		RefreshExpiresAt: refreshExpiry,
		RefreshID:        refreshID,
	}, nil
}

// ParseAccess verifies an access token and returns its claims.
func (t *Tokens) ParseAccess(raw string) (*Claims, error) { return t.parse(raw, TokenAccess) }

// ParseRefresh verifies a refresh token and returns its claims.
func (t *Tokens) ParseRefresh(raw string) (*Claims, error) { return t.parse(raw, TokenRefresh) }

func newClaims(userID, teamID uuid.UUID, kind TokenType, jti uuid.UUID, issuedAt, expiry time.Time) *Claims {
	return &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{tokenAudience},
			ID:        jti.String(),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiry),
		},
		TeamID: teamID,
		Kind:   kind,
	}
}

func (t *Tokens) sign(claims *Claims) (string, error) {
	signed, err := jwt.NewWithClaims(signingMethod, claims).SignedString(t.secret)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return signed, nil
}

// parse verifies a token's signature, registered claims, and kind.
func (t *Tokens) parse(raw string, want TokenType) (*Claims, error) {
	parser := jwt.NewParser(
		// The allowlist is what makes the alg header untrusted input rather than an
		// instruction.
		jwt.WithValidMethods([]string{signingMethod.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
	)

	claims := &Claims{}
	if _, err := parser.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return t.secret, nil
	}); err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	// A valid signature is not enough: a refresh token presented to an access-token
	// endpoint would otherwise grant a month of access from a single stolen cookie.
	if claims.Kind != want {
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrWrongTokenKind, want, claims.Kind)
	}
	if claims.TeamID == uuid.Nil {
		return nil, fmt.Errorf("%w: missing team", ErrTokenInvalid)
	}
	if _, err := claims.UserID(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	return claims, nil
}
