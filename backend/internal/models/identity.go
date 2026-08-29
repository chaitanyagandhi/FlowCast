package models

import (
	"time"

	"github.com/google/uuid"
)

// Team is FlowCast's tenant. Every other record reaches a team, directly or through its
// incident, and every protected query filters on one.
type Team struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// User is a member of exactly one team.
//
// PasswordHash is excluded from JSON rather than merely omitted when empty: a hash must
// never reach a response body, even by accident, so there is no tag that could let it
// through.
type User struct {
	ID           uuid.UUID `json:"id"`
	TeamID       uuid.UUID `json:"team_id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Provider names a telemetry source FlowCast can ingest from.
type Provider string

const (
	ProviderPagerDuty Provider = "pagerduty"
	ProviderDatadog   Provider = "datadog"
	ProviderSlack     Provider = "slack"
)

// AllProviders lists every provider, in the order the UI should present them.
func AllProviders() []Provider {
	return []Provider{ProviderPagerDuty, ProviderDatadog, ProviderSlack}
}

// Valid reports whether p is a known provider.
func (p Provider) Valid() bool { return validEnum(p, AllProviders()) }

// ParseProvider converts a path segment or request field into a Provider.
func ParseProvider(raw string) (Provider, error) {
	return parseEnum(raw, "provider", AllProviders())
}

// Integration is one team's connection to one provider. A team has at most one
// integration per provider, which is what makes it addressable by provider name.
//
// WebhookSecret verifies inbound webhook signatures and is never serialized.
type Integration struct {
	ID            uuid.UUID      `json:"id"`
	TeamID        uuid.UUID      `json:"team_id"`
	Provider      Provider       `json:"provider"`
	WebhookSecret string         `json:"-"`
	Config        map[string]any `json:"config"`
	Enabled       bool           `json:"enabled"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

const (
	maxTeamNameLen = 200
	maxUserNameLen = 200
	maxEmailLen    = 320
)

// Validate checks a team before it is written.
func (t *Team) Validate() error {
	v := &validator{}
	name := v.require("name", t.Name)
	v.maxLen("name", name, maxTeamNameLen)
	return v.err()
}

// Validate checks a user before it is written. It deliberately does not verify that the
// email is deliverable -- only that it is present, bounded, and shaped like an address.
func (u *User) Validate() error {
	v := &validator{}

	email := v.require("email", u.Email)
	v.maxLen("email", email, maxEmailLen)
	if email != "" && !ValidEmail(email) {
		v.add("email", "must be a valid email address")
	}

	name := v.require("name", u.Name)
	v.maxLen("name", name, maxUserNameLen)

	v.require("password_hash", u.PasswordHash)

	if u.TeamID == uuid.Nil {
		v.add("team_id", "is required")
	}

	return v.err()
}

// Validate checks an integration before it is written.
func (i *Integration) Validate() error {
	v := &validator{}

	enumField(v, "provider", i.Provider, AllProviders())

	if len(i.WebhookSecret) < MinWebhookSecretLen {
		v.add("webhook_secret",
			"must be at least 16 characters")
	}
	if i.TeamID == uuid.Nil {
		v.add("team_id", "is required")
	}

	return v.err()
}

// MinWebhookSecretLen matches the CHECK constraint on integrations.webhook_secret.
const MinWebhookSecretLen = 16

// ValidEmail is a deliberately loose shape check: exactly one @, with something on either
// side and a dot in the domain. Anything stricter rejects addresses that genuinely work.
// Deliverability is proven by sending mail, not by a regex.
func ValidEmail(s string) bool {
	at := -1
	for i, r := range s {
		if r == '@' {
			if at != -1 {
				return false // more than one @
			}
			at = i
		}
	}
	if at <= 0 || at == len(s)-1 {
		return false
	}
	domain := s[at+1:]
	dot := -1
	for i, r := range domain {
		if r == '.' {
			dot = i
		}
	}
	return dot > 0 && dot < len(domain)-1
}
