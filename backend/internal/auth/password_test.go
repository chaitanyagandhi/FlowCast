package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
)

// testCost keeps the suite fast. Production uses the configured default.
const testCost = bcrypt.MinCost

func testHasher(t *testing.T) *auth.Hasher {
	t.Helper()
	h, err := auth.NewHasher(testCost)
	require.NoError(t, err)
	return h
}

func TestHashAndVerifyRoundTrip(t *testing.T) {
	h := testHasher(t)
	const password = "correct-horse-battery-staple"

	hash, err := h.Hash(password)
	require.NoError(t, err)
	require.NoError(t, h.Verify(hash, password))
}

// The stored value must be a bcrypt hash, not the password in any recoverable form.
func TestHashDoesNotContainThePassword(t *testing.T) {
	h := testHasher(t)
	const password = "correct-horse-battery-staple"

	hash, err := h.Hash(password)
	require.NoError(t, err)

	require.NotContains(t, hash, password)
	require.True(t, strings.HasPrefix(hash, "$2"), "expected a bcrypt hash, got %q", hash)
}

// bcrypt salts every hash, so the same password stored twice must not produce the same
// row -- otherwise the database reveals which users share a password.
func TestSamePasswordHashesDifferentlyEachTime(t *testing.T) {
	h := testHasher(t)
	const password = "correct-horse-battery-staple"

	first, err := h.Hash(password)
	require.NoError(t, err)
	second, err := h.Hash(password)
	require.NoError(t, err)

	require.NotEqual(t, first, second, "hashes must be salted")
	require.NoError(t, h.Verify(first, password))
	require.NoError(t, h.Verify(second, password))
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	h := testHasher(t)

	hash, err := h.Hash("correct-horse-battery-staple")
	require.NoError(t, err)

	err = h.Verify(hash, "correct-horse-battery-stapl")
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

// A corrupt stored hash is a failed login to the user but must not be silently treated as
// a mismatch: the operator needs to know the row is broken.
func TestVerifyReportsMalformedHashDistinctly(t *testing.T) {
	h := testHasher(t)

	err := h.Verify("not-a-bcrypt-hash", "correct-horse-battery-staple")
	require.Error(t, err)
	require.NotErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestVerifyDummyAlwaysFails(t *testing.T) {
	h := testHasher(t)
	require.ErrorIs(t, h.VerifyDummy(), auth.ErrInvalidCredentials)
}

// Login must cost about the same whether or not the address is registered, or response
// time alone enumerates accounts. This asserts the dummy path does real bcrypt work
// rather than returning instantly.
func TestVerifyDummyCostsComparableTimeToARealVerify(t *testing.T) {
	// A cost low enough to keep the test quick but high enough to be measurable.
	h, err := auth.NewHasher(10)
	require.NoError(t, err)

	hash, err := h.Hash("correct-horse-battery-staple")
	require.NoError(t, err)

	realStart := time.Now()
	require.ErrorIs(t, h.Verify(hash, "wrong-password-entirely"), auth.ErrInvalidCredentials)
	realElapsed := time.Since(realStart)

	dummyStart := time.Now()
	require.ErrorIs(t, h.VerifyDummy(), auth.ErrInvalidCredentials)
	dummyElapsed := time.Since(dummyStart)

	// Generous bounds: the point is that the dummy path is not orders of magnitude
	// faster, which is what a bare `return ErrInvalidCredentials` would be.
	require.Greater(t, dummyElapsed*4, realElapsed,
		"the unknown-user path returns far too quickly and leaks which accounts exist")
}

func TestValidatePasswordAcceptsAReasonablePassword(t *testing.T) {
	require.NoError(t, auth.ValidatePassword("correct-horse-battery-staple"))
	require.NoError(t, auth.ValidatePassword(strings.Repeat("a", auth.MinPasswordLength)))
}

func TestValidatePasswordRejectsShortAndEmpty(t *testing.T) {
	for _, password := range []string{"", "short", strings.Repeat("a", auth.MinPasswordLength-1)} {
		err := auth.ValidatePassword(password)
		require.Error(t, err, "password %q should be rejected", password)

		var verr *models.ValidationError
		require.ErrorAs(t, err, &verr)
		require.Equal(t, "password", verr.Issues[0].Field)
	}
}

// bcrypt hashes only the first 72 bytes. Accepting a longer password would silently
// ignore the tail, so two passwords sharing a 72-byte prefix would be interchangeable.
func TestValidatePasswordRejectsOverBcryptLimit(t *testing.T) {
	tooLong := strings.Repeat("a", auth.MaxPasswordBytes+1)

	err := auth.ValidatePassword(tooLong)
	require.Error(t, err)
	require.Contains(t, err.Error(), "72 bytes")
}

// The limit is on encoded bytes, so a password well under 72 characters can still exceed
// it. This is the case a rune-based check would get wrong.
func TestValidatePasswordMeasuresBytesNotRunes(t *testing.T) {
	// 20 four-byte runes: 20 characters, 80 bytes.
	emoji := strings.Repeat("😀", 20)
	require.Len(t, []rune(emoji), 20)
	require.Greater(t, len(emoji), auth.MaxPasswordBytes)

	require.Error(t, auth.ValidatePassword(emoji))
}

func TestHashRejectsAPasswordThatFailsPolicy(t *testing.T) {
	h := testHasher(t)

	_, err := h.Hash("short")
	require.Error(t, err)

	var verr *models.ValidationError
	require.ErrorAs(t, err, &verr)
}

func TestNewHasherRejectsCostOutsideBcryptRange(t *testing.T) {
	for _, cost := range []int{bcrypt.MinCost - 1, bcrypt.MaxCost + 1} {
		_, err := auth.NewHasher(cost)
		require.Error(t, err, "cost %d should be rejected", cost)
	}
}

func TestHasherReportsItsCost(t *testing.T) {
	h := testHasher(t)
	require.Equal(t, testCost, h.Cost())

	hash, err := h.Hash("correct-horse-battery-staple")
	require.NoError(t, err)

	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	require.Equal(t, testCost, cost, "the hash must record the configured work factor")
}
