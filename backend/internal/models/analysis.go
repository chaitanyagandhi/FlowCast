package models

import (
	"time"

	"github.com/google/uuid"
)

// MaxHypotheses caps how many ranked alternatives one analysis may produce. Beyond three
// the tail is guesswork, and the schema enforces the same limit.
const MaxHypotheses = 3

// IncidentAnalysis is one diagnosis of one incident.
//
// Analyses are append-only: re-analyzing an incident records a new row alongside the old
// one, tagged with the model and prompt version that produced it. That history is what
// makes it possible to say later whether a prompt change actually helped.
type IncidentAnalysis struct {
	ID         uuid.UUID `json:"id"`
	IncidentID uuid.UUID `json:"incident_id"`

	PredictedRootCause string  `json:"predicted_root_cause"`
	Confidence         float32 `json:"confidence"`

	// ReasoningSummary is a short, user-facing explanation. Hidden chain-of-thought is
	// never requested, stored, or exposed.
	ReasoningSummary string `json:"reasoning_summary"`

	Model         string `json:"model"`
	PromptVersion string `json:"prompt_version"`

	CreatedAt time.Time `json:"created_at"`

	// Hypotheses is the ranked alternative set, loaded alongside the analysis.
	Hypotheses []RootCauseHypothesis `json:"hypotheses,omitempty"`
}

// RootCauseHypothesis is one candidate explanation, ranked against its alternatives.
type RootCauseHypothesis struct {
	ID         uuid.UUID `json:"id"`
	AnalysisID uuid.UUID `json:"analysis_id"`

	Rank       int     `json:"rank"`
	Cause      string  `json:"cause"`
	Confidence float32 `json:"confidence"`

	// EvidenceEventIDs cites the events supporting this hypothesis. Every ID is checked
	// against the incident's real events before the analysis is accepted, so a model
	// cannot support a conclusion with evidence it invented.
	EvidenceEventIDs []uuid.UUID `json:"evidence_event_ids"`

	CreatedAt time.Time `json:"created_at"`
}

// Top returns the highest-ranked hypothesis, if there is one.
func (a *IncidentAnalysis) Top() (RootCauseHypothesis, bool) {
	best := -1
	for i, h := range a.Hypotheses {
		if best == -1 || h.Rank < a.Hypotheses[best].Rank {
			best = i
		}
	}
	if best == -1 {
		return RootCauseHypothesis{}, false
	}
	return a.Hypotheses[best], true
}

// SortHypotheses orders hypotheses best-first.
func (a *IncidentAnalysis) SortHypotheses() {
	sortSlice(a.Hypotheses, func(x, y RootCauseHypothesis) bool { return x.Rank < y.Rank })
}

// Validate checks an analysis, including its hypotheses, before it is written.
func (a *IncidentAnalysis) Validate() error {
	v := &validator{}

	v.require("predicted_root_cause", a.PredictedRootCause)
	v.require("model", a.Model)
	v.require("prompt_version", a.PromptVersion)

	if a.IncidentID == uuid.Nil {
		v.add("incident_id", "is required")
	}
	validateConfidence(v, "confidence", a.Confidence)

	if len(a.Hypotheses) > MaxHypotheses {
		v.add("hypotheses", "must contain at most 3 entries")
	}

	seenRanks := make(map[int]bool, len(a.Hypotheses))
	for _, h := range a.Hypotheses {
		if seenRanks[h.Rank] {
			v.add("hypotheses", "ranks must be distinct")
			break
		}
		seenRanks[h.Rank] = true
	}

	return v.err()
}

// Validate checks a single hypothesis.
func (h *RootCauseHypothesis) Validate() error {
	v := &validator{}

	v.require("cause", h.Cause)
	validateConfidence(v, "confidence", h.Confidence)

	if h.Rank < 1 || h.Rank > MaxHypotheses {
		v.add("rank", "must be between 1 and 3")
	}
	for _, id := range h.EvidenceEventIDs {
		if id == uuid.Nil {
			v.add("evidence_event_ids", "must not contain an empty id")
			break
		}
	}

	return v.err()
}

// validateConfidence enforces that a confidence reads as a probability.
func validateConfidence(v *validator, field string, value float32) {
	if value < 0 || value > 1 {
		v.add(field, "must be between 0 and 1")
	}
}
