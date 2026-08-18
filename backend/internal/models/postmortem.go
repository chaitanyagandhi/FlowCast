package models

import (
	"time"

	"github.com/google/uuid"
)

// ActionItem is one piece of follow-up work the postmortem recommends.
type ActionItem struct {
	Description string `json:"description"`
	// Owner is the team or person expected to do it, empty when the generator could not
	// responsibly guess one.
	Owner string `json:"owner,omitempty"`
}

// Postmortem is the written record of an incident: generated from the analysis, then
// editable by a human, which is why it is stored separately from the analysis that seeded
// it. There is at most one per incident.
type Postmortem struct {
	ID         uuid.UUID `json:"id"`
	IncidentID uuid.UUID `json:"incident_id"`

	ExecutiveSummary string `json:"executive_summary"`
	Impact           string `json:"impact"`
	RootCause        string `json:"root_cause"`
	TimelineMD       string `json:"timeline_md"`

	ContributingFactors []string     `json:"contributing_factors"`
	ActionItems         []ActionItem `json:"action_items"`

	// Uncertainties is where the generator states what it could not establish. It is a
	// first-class field so an honest "we do not know" survives into the document instead
	// of being smoothed away.
	Uncertainties []string `json:"uncertainties"`

	GeneratedAt time.Time `json:"generated_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Validate checks a postmortem before it is written. Individual sections may be empty --
// a thin postmortem is better than an invented one -- but it must belong to an incident
// and say something.
func (p *Postmortem) Validate() error {
	v := &validator{}

	if p.IncidentID == uuid.Nil {
		v.add("incident_id", "is required")
	}
	if p.ExecutiveSummary == "" && p.RootCause == "" && p.Impact == "" {
		v.add("executive_summary", "at least one section must be filled in")
	}
	for _, item := range p.ActionItems {
		if item.Description == "" {
			v.add("action_items", "every action item needs a description")
			break
		}
	}

	return v.err()
}
