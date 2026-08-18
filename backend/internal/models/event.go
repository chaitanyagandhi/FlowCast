package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Classification is what the AI stage decided an event represents within the incident.
type Classification string

const (
	// ClassRootCause is the event that started the failure.
	ClassRootCause Classification = "ROOT_CAUSE"
	// ClassContributingFactor made the failure possible or worse without causing it.
	ClassContributingFactor Classification = "CONTRIBUTING_FACTOR"
	// ClassSymptom is a downstream effect: the alerts and errors an incident produces.
	ClassSymptom Classification = "SYMPTOM"
	// ClassMitigation is an action taken to reduce impact.
	ClassMitigation Classification = "MITIGATION"
	// ClassRecovery marks the system returning to health.
	ClassRecovery Classification = "RECOVERY"
	// ClassNoise is unrelated to this incident, however suggestive its timing.
	ClassNoise Classification = "NOISE"
	// ClassUnknown is the state every event starts in, before classification runs.
	ClassUnknown Classification = "UNKNOWN"
)

func AllClassifications() []Classification {
	return []Classification{
		ClassRootCause, ClassContributingFactor, ClassSymptom,
		ClassMitigation, ClassRecovery, ClassNoise, ClassUnknown,
	}
}

func (c Classification) Valid() bool { return validEnum(c, AllClassifications()) }

func ParseClassification(raw string) (Classification, error) {
	return parseEnum(raw, "classification", AllClassifications())
}

// IsCausal reports whether the classification puts the event on the causal chain, as
// opposed to being an effect, a response, or unrelated. Causal precision and recall are
// measured over exactly this set.
func (c Classification) IsCausal() bool {
	return c == ClassRootCause || c == ClassContributingFactor
}

// Event is the canonical form every integration normalizes into. Whatever shape PagerDuty,
// Datadog, or Slack sent, the correlation and AI stages see only this.
type Event struct {
	ID         uuid.UUID `json:"id"`
	IncidentID uuid.UUID `json:"incident_id"`

	Source      Source `json:"source"`
	EventType   string `json:"event_type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Service     string `json:"service"`

	OccurredAt time.Time `json:"occurred_at"`

	// RawPayload keeps the provider's original message verbatim, so normalization is
	// never lossy and an adapter bug can be diagnosed after the fact. Not serialized to
	// API clients, which want the normalized view.
	RawPayload json.RawMessage `json:"-"`

	Classification Classification `json:"classification"`
	// CausalRank is the event's position in the reconstructed chain, nil when it is not
	// on the chain at all.
	CausalRank *int `json:"causal_rank,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

const (
	maxEventTypeLen = 100
	maxEventTitle   = 500
)

// Validate checks an event before it is written.
func (e *Event) Validate() error {
	v := &validator{}

	eventType := v.require("event_type", e.EventType)
	v.maxLen("event_type", eventType, maxEventTypeLen)

	title := v.require("title", e.Title)
	v.maxLen("title", title, maxEventTitle)

	enumField(v, "source", e.Source, AllSources())
	enumField(v, "classification", e.Classification, AllClassifications())

	if e.IncidentID == uuid.Nil {
		v.add("incident_id", "is required")
	}
	if e.OccurredAt.IsZero() {
		v.add("occurred_at", "is required")
	}
	if e.CausalRank != nil && *e.CausalRank < 1 {
		v.add("causal_rank", "must be positive when set")
	}

	return v.err()
}

// SortEventsByTime orders events chronologically, breaking ties by ID so the ordering is
// deterministic. The AI stages depend on a stable order: the same incident must produce
// the same prompt every time, or results are not comparable between runs.
func SortEventsByTime(events []Event) {
	sortSlice(events, func(a, b Event) bool {
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		return a.ID.String() < b.ID.String()
	})
}
