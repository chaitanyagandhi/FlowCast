package auth

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
)

// Password length bounds.
//
// The minimum follows current NIST guidance: length does far more for a password than
// forced character classes, which mostly push people towards Passw0rd! and a sticky note.
// The maximum is not a policy choice -- bcrypt hashes only the first 72 bytes, so a longer
// password would have its tail silently ignored, and two passwords sharing a 72-byte
// prefix would be interchangeable.
const (
	MinPasswordLength = 12
	MaxPasswordBytes  = 72
)

// ErrInvalidCredentials is returned when a password does not match its hash.
//
// It says nothing about which half was wrong. Callers must not distinguish "no such user"
// from "wrong password" in what they return, or the login endpoint becomes a way to
// enumerate registered email addresses.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Hasher hashes and verifies passwords with bcrypt at a fixed cost.
type Hasher struct {
	cost int
	// dummyHash is compared against when no user was found, so a login for an unknown
	// address costs the same time as one for a known address. Without it, the response
	// time alone reveals which addresses are registered.
	dummyHash []byte
}

// NewHasher builds a Hasher at the given bcrypt cost.
func NewHasher(cost int) (*Hasher, error) {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return nil, fmt.Errorf("bcrypt cost must be between %d and %d, got %d",
			bcrypt.MinCost, bcrypt.MaxCost, cost)
	}

	// Hashed once at construction, at the same cost as a real password, so the timing
	// matches.
	dummy, err := bcrypt.GenerateFromPassword([]byte("flowcast-timing-equalizer"), cost)
	if err != nil {
		return nil, fmt.Errorf("preparing password hasher: %w", err)
	}

	return &Hasher{cost: cost, dummyHash: dummy}, nil
}

// Cost reports the configured bcrypt work factor.
func (h *Hasher) Cost() int { return h.cost }

// Hash returns the bcrypt hash of a password that has already passed ValidatePassword.
func (h *Hasher) Hash(plain string) (string, error) {
	if err := ValidatePassword(plain); err != nil {
		return "", err
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hashed), nil
}

// Verify reports whether plain matches hash, returning ErrInvalidCredentials if not.
func (h *Hasher) Verify(hash, plain string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ErrInvalidCredentials
		}
		// A malformed or truncated stored hash. Still a failed login to the caller, but
		// the operator needs to know the row is corrupt.
		return fmt.Errorf("comparing password hash: %w", err)
	}
	return nil
}

// VerifyDummy burns the same work as a real verification and always fails.
//
// Call it when no user matched, so an attacker cannot tell a registered address from an
// unregistered one by timing the response.
func (h *Hasher) VerifyDummy() error {
	_ = bcrypt.CompareHashAndPassword(h.dummyHash, []byte("not-the-password"))
	return ErrInvalidCredentials
}

// ValidatePassword checks a password against policy before it is hashed.
func ValidatePassword(plain string) error {
	var issues []models.FieldIssue

	switch {
	case plain == "":
		issues = append(issues, models.FieldIssue{
			Field: "password", Message: "is required",
		})
	case utf8.RuneCountInString(plain) < MinPasswordLength:
		issues = append(issues, models.FieldIssue{
			Field:   "password",
			Message: fmt.Sprintf("must be at least %d characters", MinPasswordLength),
		})
	}

	// Measured in bytes, not runes: bcrypt's limit is on the encoded length, so an
	// emoji-heavy password hits it sooner than its character count suggests.
	if len(plain) > MaxPasswordBytes {
		issues = append(issues, models.FieldIssue{
			Field:   "password",
			Message: fmt.Sprintf("must be at most %d bytes", MaxPasswordBytes),
		})
	}

	if len(issues) == 0 {
		return nil
	}
	return &models.ValidationError{Issues: issues}
}
