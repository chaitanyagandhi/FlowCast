package auth_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

const testSecret = "0123456789abcdef0123456789abcdef-flowcast"

func authConfig() config.AuthConfig {
	return config.AuthConfig{
		JWTSecret:       testSecret,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
		BcryptCost:      testCost,
	}
}

func testTokens(t *testing.T, opts ...auth.Option) *auth.Tokens {
	t.Helper()
	tokens, err := auth.NewTokens(authConfig(), opts...)
	require.NoError(t, err)
	return tokens
}

// frozenClock returns a clock the test controls, so expiry needs no sleeping.
func frozenClock(at *time.Time) auth.Option {
	return auth.WithClock(func() time.Time { return *at })
}

func TestIssuePairProducesUsableTokens(t *testing.T) {
	tokens := testTokens(t)
	userID, teamID := uuid.New(), uuid.New()

	pair, err := tokens.IssuePair(userID, teamID)
	require.NoError(t, err)

	require.NotEmpty(t, pair.AccessToken)
	require.NotEmpty(t, pair.RefreshToken)
	require.NotEqual(t, pair.AccessToken, pair.RefreshToken)
	require.True(t, pair.AccessExpiresAt.Before(pair.RefreshExpiresAt))
	require.NotEqual(t, uuid.Nil, pair.RefreshID)

	claims, err := tokens.ParseAccess(pair.AccessToken)
	require.NoError(t, err)

	gotUser, err := claims.UserID()
	require.NoError(t, err)
	require.Equal(t, userID, gotUser)
	require.Equal(t, teamID, claims.TeamID, "tenant scoping depends on this claim")
	require.Equal(t, auth.TokenAccess, claims.Kind)
}

func TestRefreshTokenParsesAsRefresh(t *testing.T) {
	tokens := testTokens(t)
	userID, teamID := uuid.New(), uuid.New()

	pair, err := tokens.IssuePair(userID, teamID)
	require.NoError(t, err)

	claims, err := tokens.ParseRefresh(pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, auth.TokenRefresh, claims.Kind)
	require.Equal(t, pair.RefreshID.String(), claims.ID,
		"the jti must match so logout can revoke this specific token")
}

// A refresh token accepted as an access token would turn one stolen cookie into a month
// of API access.
func TestRefreshTokenIsRejectedAsAnAccessToken(t *testing.T) {
	tokens := testTokens(t)

	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	_, err = tokens.ParseAccess(pair.RefreshToken)
	require.ErrorIs(t, err, auth.ErrWrongTokenKind)
}

func TestAccessTokenIsRejectedAsARefreshToken(t *testing.T) {
	tokens := testTokens(t)

	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	_, err = tokens.ParseRefresh(pair.AccessToken)
	require.ErrorIs(t, err, auth.ErrWrongTokenKind)
}

func TestExpiredAccessTokenIsRejected(t *testing.T) {
	now := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)
	tokens := testTokens(t, frozenClock(&now))

	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	// Still valid one minute before it expires.
	now = now.Add(14 * time.Minute)
	_, err = tokens.ParseAccess(pair.AccessToken)
	require.NoError(t, err)

	// Expired a minute after.
	now = now.Add(2 * time.Minute)
	_, err = tokens.ParseAccess(pair.AccessToken)
	require.ErrorIs(t, err, auth.ErrTokenExpired)
}

func TestExpiredRefreshTokenIsRejected(t *testing.T) {
	now := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)
	tokens := testTokens(t, frozenClock(&now))

	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	now = now.Add(721 * time.Hour)
	_, err = tokens.ParseRefresh(pair.RefreshToken)
	require.ErrorIs(t, err, auth.ErrTokenExpired)
}

func TestTokenSignedWithAnotherSecretIsRejected(t *testing.T) {
	tokens := testTokens(t)

	otherCfg := authConfig()
	otherCfg.JWTSecret = "ffffffffffffffffffffffffffffffff-other"
	other, err := auth.NewTokens(otherCfg)
	require.NoError(t, err)

	pair, err := other.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	_, err = tokens.ParseAccess(pair.AccessToken)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestTamperedTokenIsRejected(t *testing.T) {
	tokens := testTokens(t)

	pair, err := tokens.IssuePair(uuid.New(), uuid.New())
	require.NoError(t, err)

	parts := strings.Split(pair.AccessToken, ".")
	require.Len(t, parts, 3)

	// Rewrite the payload to claim a different team, keeping the original signature.
	var claims map[string]any
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(payload, &claims))
	claims["team_id"] = uuid.New().String()

	forged, err := json.Marshal(claims)
	require.NoError(t, err)
	parts[1] = base64.RawURLEncoding.EncodeToString(forged)

	_, err = tokens.ParseAccess(strings.Join(parts, "."))
	require.ErrorIs(t, err, auth.ErrTokenInvalid,
		"a rewritten team claim must not survive signature verification")
}

// The classic JWT attack: strip the signature and set alg to none.
//
// golang-jwt v5 already refuses this by default -- it demands an explicit unsafe key type
// to even produce such a token -- so this passes without our algorithm allowlist. It is
// kept as a regression guard in case the library's default ever softens or the parser is
// rebuilt by hand. The allowlist is what stops the case below.
func TestUnsignedNoneAlgorithmTokenIsRejected(t *testing.T) {
	tokens := testTokens(t)

	claims := jwt.MapClaims{
		"sub":     uuid.New().String(),
		"team_id": uuid.New().String(),
		"typ":     string(auth.TokenAccess),
		"iss":     "flowcast",
		"aud":     "flowcast-api",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	_, err = tokens.ParseAccess(unsigned)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

// Algorithm confusion, and the reason the allowlist exists: without
// jwt.WithValidMethods, an HS512 token signed with our own secret verifies cleanly and is
// accepted. Removing the allowlist makes this test fail, which is exactly the point --
// the accepted algorithm is our decision, not the token's.
func TestOtherHMACAlgorithmsAreRejected(t *testing.T) {
	tokens := testTokens(t)

	for _, method := range []jwt.SigningMethod{jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		claims := jwt.MapClaims{
			"sub":     uuid.New().String(),
			"team_id": uuid.New().String(),
			"typ":     string(auth.TokenAccess),
			"iss":     "flowcast",
			"aud":     "flowcast-api",
			"exp":     time.Now().Add(time.Hour).Unix(),
		}
		signed, err := jwt.NewWithClaims(method, claims).SignedString([]byte(testSecret))
		require.NoError(t, err)

		_, err = tokens.ParseAccess(signed)
		require.ErrorIs(t, err, auth.ErrTokenInvalid, "%s should be refused", method.Alg())
	}
}

// A token minted for another system that happens to share our signing key must not be
// accepted here.
func TestTokenFromAnotherIssuerOrAudienceIsRejected(t *testing.T) {
	tokens := testTokens(t)

	base := func() jwt.MapClaims {
		return jwt.MapClaims{
			"sub":     uuid.New().String(),
			"team_id": uuid.New().String(),
			"typ":     string(auth.TokenAccess),
			"iss":     "flowcast",
			"aud":     "flowcast-api",
			"exp":     time.Now().Add(time.Hour).Unix(),
		}
	}

	wrongIssuer := base()
	wrongIssuer["iss"] = "some-other-service"

	wrongAudience := base()
	wrongAudience["aud"] = "some-other-api"

	for name, claims := range map[string]jwt.MapClaims{
		"issuer": wrongIssuer, "audience": wrongAudience,
	} {
		t.Run(name, func(t *testing.T) {
			signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
				SignedString([]byte(testSecret))
			require.NoError(t, err)

			_, err = tokens.ParseAccess(signed)
			require.ErrorIs(t, err, auth.ErrTokenInvalid)
		})
	}
}

// Without an expiry a stolen token would be valid forever.
func TestTokenWithoutExpiryIsRejected(t *testing.T) {
	tokens := testTokens(t)

	claims := jwt.MapClaims{
		"sub":     uuid.New().String(),
		"team_id": uuid.New().String(),
		"typ":     string(auth.TokenAccess),
		"iss":     "flowcast",
		"aud":     "flowcast-api",
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)

	_, err = tokens.ParseAccess(signed)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

// Tenant scoping reads team_id straight off the token, so a token without one must never
// reach a handler.
func TestTokenWithoutTeamIsRejected(t *testing.T) {
	tokens := testTokens(t)

	claims := jwt.MapClaims{
		"sub": uuid.New().String(),
		"typ": string(auth.TokenAccess),
		"iss": "flowcast",
		"aud": "flowcast-api",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)

	_, err = tokens.ParseAccess(signed)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestTokenWithNonUUIDSubjectIsRejected(t *testing.T) {
	tokens := testTokens(t)

	claims := jwt.MapClaims{
		"sub":     "admin",
		"team_id": uuid.New().String(),
		"typ":     string(auth.TokenAccess),
		"iss":     "flowcast",
		"aud":     "flowcast-api",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)

	_, err = tokens.ParseAccess(signed)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestGarbageIsRejected(t *testing.T) {
	tokens := testTokens(t)

	for _, raw := range []string{"", "not-a-token", "a.b.c", strings.Repeat("x", 500)} {
		_, err := tokens.ParseAccess(raw)
		require.Error(t, err, "%q should be rejected", raw)
	}
}

// Each issued pair carries its own jti, so revoking one session cannot revoke another.
func TestEachPairHasADistinctRefreshID(t *testing.T) {
	tokens := testTokens(t)
	userID, teamID := uuid.New(), uuid.New()

	first, err := tokens.IssuePair(userID, teamID)
	require.NoError(t, err)
	second, err := tokens.IssuePair(userID, teamID)
	require.NoError(t, err)

	require.NotEqual(t, first.RefreshID, second.RefreshID)
	require.NotEqual(t, first.RefreshToken, second.RefreshToken)
}

func TestIssuePairRequiresUserAndTeam(t *testing.T) {
	tokens := testTokens(t)

	_, err := tokens.IssuePair(uuid.Nil, uuid.New())
	require.Error(t, err)

	_, err = tokens.IssuePair(uuid.New(), uuid.Nil)
	require.Error(t, err)
}

func TestNewTokensRejectsUnsafeConfiguration(t *testing.T) {
	tests := map[string]func(*config.AuthConfig){
		"short secret":            func(c *config.AuthConfig) { c.JWTSecret = "tooshort" },
		"zero access ttl":         func(c *config.AuthConfig) { c.AccessTokenTTL = 0 },
		"zero refresh ttl":        func(c *config.AuthConfig) { c.RefreshTokenTTL = 0 },
		"access outlives refresh": func(c *config.AuthConfig) { c.AccessTokenTTL = c.RefreshTokenTTL },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := authConfig()
			mutate(&cfg)
			_, err := auth.NewTokens(cfg)
			require.Error(t, err)
		})
	}
}
