package models

import (
	"time"

	"github.com/google/uuid"
)

// Severity is the incident's impact level, using the conventional P1-P4 scale.
type Severity string

const (
	SeverityP1 Severity = "P1" // critical
	SeverityP2 Severity = "P2"
	SeverityP3 Severity = "P3"
	SeverityP4 Severity = "P4" // minor
)

// AllSeverities lists severities from most to least severe.
func AllSeverities() []Severity {
	return []Severity{SeverityP1, SeverityP2, SeverityP3, SeverityP4}
}

func (s Severity) Valid() bool { return validEnum(s, AllSeverities()) }

func ParseSeverity(raw string) (Severity, error) {
	return parseEnum(raw, "severity", AllSeverities())
}

// IncidentStatus tracks an incident through ingestion, analysis, and resolution.
type IncidentStatus string

const (
	// StatusOpen is an incident that has been recorded but not analyzed.
	StatusOpen IncidentStatus = "open"
	// StatusProcessing means analysis has been queued or is running.
	StatusProcessing IncidentStatus = "processing"
	// StatusAnalysisReady means a diagnosis exists and can be shown.
	StatusAnalysisReady IncidentStatus = "analysis_ready"
	// StatusResolved means the underlying problem is over.
	StatusResolved IncidentStatus = "resolved"
)

func AllIncidentStatuses() []IncidentStatus {
	return []IncidentStatus{StatusOpen, StatusProcessing, StatusAnalysisReady, StatusResolved}
}

func (s IncidentStatus) Valid() bool { return validEnum(s, AllIncidentStatuses()) }

func ParseIncidentStatus(raw string) (IncidentStatus, error) {
	return parseEnum(raw, "status", AllIncidentStatuses())
}

// Source identifies where an incident or event came from. The prompt's EventSource and an
// incident's source share one type because they share one set of values, and a single
// definition cannot drift against itself.
type Source string

const (
	SourceManual     Source = "manual"
	SourcePagerDuty  Source = "pagerduty"
	SourceDatadog    Source = "datadog"
	SourceSlack      Source = "slack"
	SourceSimulation Source = "simulation"
)

func AllSources() []Source {
	return []Source{SourceManual, SourcePagerDuty, SourceDatadog, SourceSlack, SourceSimulation}
}

func (s Source) Valid() bool { return validEnum(s, AllSources()) }

func ParseSource(raw string) (Source, error) {
	return parseEnum(raw, "source", AllSources())
}

// SourceForProvider maps an integration provider onto the source its events carry.
func SourceForProvider(p Provider) Source { return Source(p) }

// Incident is one production failure: the thing FlowCast reconstructs a causal chain for.
type Incident struct {
	ID     uuid.UUID `json:"id"`
	TeamID uuid.UUID `json:"team_id"`

	// ExternalID is the originating system's identifier, absent for incidents created
	// by hand. Unique per team when present, which is what makes redelivered webhooks
	// attach to the existing incident instead of creating a second one.
	ExternalID *string `json:"external_id,omitempty"`

	Title       string         `json:"title"`
	Description string         `json:"description"`
	Severity    Severity       `json:"severity"`
	Status      IncidentStatus `json:"status"`
	Source      Source         `json:"source"`

	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	// Embedding of title, summary, and predicted root cause, used for similar-incident
	// retrieval. Kept as a plain slice so the domain does not depend on the pgvector
	// types; the repository converts. Never serialized -- it is machinery, not content.
	Embedding []float32 `json:"-"`

	Metadata map[string]any `json:"metadata,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Duration reports how long the incident lasted, and whether it is over.
func (i *Incident) Duration() (time.Duration, bool) {
	if i.ResolvedAt == nil {
		return 0, false
	}
	return i.ResolvedAt.Sub(i.StartedAt), true
}

// IsOpen reports whether the incident is still unresolved.
func (i *Incident) IsOpen() bool { return i.Status != StatusResolved }

const maxIncidentTitleLen = 500

// Validate checks an incident before it is written.
func (i *Incident) Validate() error {
	v := &validator{}

	title := v.require("title", i.Title)
	v.maxLen("title", title, maxIncidentTitleLen)

	enumField(v, "severity", i.Severity, AllSeverities())
	enumField(v, "status", i.Status, AllIncidentStatuses())
	enumField(v, "source", i.Source, AllSources())

	if i.TeamID == uuid.Nil {
		v.add("team_id", "is required")
	}
	if i.StartedAt.IsZero() {
		v.add("started_at", "is required")
	}
	if i.ResolvedAt != nil && i.ResolvedAt.Before(i.StartedAt) {
		v.add("resolved_at", "must not be before started_at")
	}
	if i.ExternalID != nil && *i.ExternalID == "" {
		v.add("external_id", "must be absent rather than empty")
	}

	return v.err()
}
