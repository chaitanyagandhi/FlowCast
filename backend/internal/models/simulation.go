package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// FaultAction is a fault the simulator knows how to inject. This list is an allowlist, not
// a suggestion: a scenario naming anything outside it is rejected before execution, which
// is what stops an AI-proposed scenario from becoming arbitrary code.
type FaultAction string

const (
	FaultAddLatency     FaultAction = "ADD_LATENCY"
	FaultReturnErrors   FaultAction = "RETURN_ERRORS"
	FaultStopDependency FaultAction = "STOP_DEPENDENCY"
	FaultExhaustDBPool  FaultAction = "EXHAUST_DB_POOL"
	FaultSlowWorker     FaultAction = "SLOW_WORKER"
	FaultBadDeployment  FaultAction = "BAD_DEPLOYMENT"
)

func AllFaultActions() []FaultAction {
	return []FaultAction{
		FaultAddLatency, FaultReturnErrors, FaultStopDependency,
		FaultExhaustDBPool, FaultSlowWorker, FaultBadDeployment,
	}
}

func (f FaultAction) Valid() bool { return validEnum(f, AllFaultActions()) }

func ParseFaultAction(raw string) (FaultAction, error) {
	return parseEnum(raw, "fault action", AllFaultActions())
}

// ScenarioConfig describes the fault to inject: which service, which action, and with
// what parameters.
type ScenarioConfig struct {
	Target     string         `json:"target"`
	Fault      FaultAction    `json:"fault"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Validate checks the shape of a scenario configuration. Parameter ranges are checked by
// the simulation package, which knows what each fault accepts.
func (c *ScenarioConfig) Validate() error {
	v := &validator{}
	v.require("target", c.Target)
	enumField(v, "fault", c.Fault, AllFaultActions())
	return v.err()
}

// SimulationScenario is a reproducible experiment: a fault to inject and the answer an
// ideal diagnosis would give.
//
// RootCause is ground truth. It must never reach the analysis pipeline.
type SimulationScenario struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Description string    `json:"description"`

	// RootCause is withheld from API responses until a run's result is revealed, so the
	// answer cannot be read off the scenario list while a run is in flight.
	RootCause string `json:"-"`

	Config    ScenarioConfig `json:"scenario_config"`
	CreatedAt time.Time      `json:"created_at"`
}

// RunStatus tracks a simulation from queued to scored.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunAnalyzing RunStatus = "analyzing"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
)

func AllRunStatuses() []RunStatus {
	return []RunStatus{RunPending, RunRunning, RunAnalyzing, RunCompleted, RunFailed}
}

func (s RunStatus) Valid() bool { return validEnum(s, AllRunStatuses()) }

func ParseRunStatus(raw string) (RunStatus, error) {
	return parseEnum(raw, "run status", AllRunStatuses())
}

// Terminal reports whether the run has finished, successfully or not.
func (s RunStatus) Terminal() bool { return s == RunCompleted || s == RunFailed }

// GroundTruth is what actually happened, recorded by the simulator before FlowCast sees
// anything. The evaluation package compares a prediction against this; no other package
// should read it.
type GroundTruth struct {
	RootCause string `json:"root_cause"`
	// CausalEventIDs are the events genuinely on the causal chain, used for causal
	// precision and recall.
	CausalEventIDs []uuid.UUID `json:"causal_event_ids,omitempty"`
	// NoiseEventIDs are the events injected as distractions, used for noise rejection.
	NoiseEventIDs []uuid.UUID `json:"noise_event_ids,omitempty"`
	// NoiseLevel is the fraction of injected noise, for robustness experiments.
	NoiseLevel float64 `json:"noise_level"`
}

// SimulationRun is one execution of a scenario.
type SimulationRun struct {
	ID         uuid.UUID `json:"id"`
	ScenarioID uuid.UUID `json:"scenario_id"`
	TeamID     uuid.UUID `json:"team_id"`

	// IncidentID is set once the generated telemetry has produced an incident.
	IncidentID *uuid.UUID `json:"incident_id,omitempty"`

	Status      RunStatus  `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// GroundTruth is snapshotted at execution time so editing the scenario afterwards
	// cannot rewrite what this run was judged against. Withheld from JSON: the API
	// reveals it through an explicit endpoint, after the prediction is in.
	GroundTruth GroundTruth `json:"-"`

	CreatedAt time.Time `json:"created_at"`
}

// EvaluationResult is how one run's prediction scored against its ground truth.
//
// The three ratio metrics are pointers because they are genuinely undefined for some
// runs -- precision when nothing was predicted, recall when there was nothing to find.
// A nil metric means "not measurable", which is not the same as zero.
type EvaluationResult struct {
	ID              uuid.UUID `json:"id"`
	SimulationRunID uuid.UUID `json:"simulation_run_id"`

	RootCauseCorrect bool `json:"root_cause_correct"`
	// RootCauseRank is where the true cause appeared among the ranked hypotheses, nil
	// when it did not appear at all. Top-3 accuracy counts the non-nil ranks.
	RootCauseRank *int `json:"root_cause_rank,omitempty"`

	CausalPrecision *float32 `json:"causal_precision,omitempty"`
	CausalRecall    *float32 `json:"causal_recall,omitempty"`
	NoiseAccuracy   *float32 `json:"noise_accuracy,omitempty"`

	DiagnosisLatencyMS int `json:"diagnosis_latency_ms"`

	CreatedAt time.Time `json:"created_at"`
}

const maxScenarioNameLen = 200

// Validate checks a scenario before it is written.
func (s *SimulationScenario) Validate() error {
	v := &validator{}

	name := v.require("name", s.Name)
	v.maxLen("name", name, maxScenarioNameLen)
	v.require("category", s.Category)

	// Without a stated root cause a run cannot be scored, so it is not an experiment.
	v.require("root_cause", s.RootCause)

	// Surface the config's own issues under a prefixed field name, so a caller sees one
	// flat list rather than a nested error.
	var configErr *ValidationError
	if err := s.Config.Validate(); errors.As(err, &configErr) {
		for _, issue := range configErr.Issues {
			v.add("scenario_config."+issue.Field, issue.Message)
		}
	}

	return v.err()
}

// Validate checks a run before it is written.
func (r *SimulationRun) Validate() error {
	v := &validator{}

	enumField(v, "status", r.Status, AllRunStatuses())

	if r.ScenarioID == uuid.Nil {
		v.add("scenario_id", "is required")
	}
	if r.TeamID == uuid.Nil {
		v.add("team_id", "is required")
	}
	if r.CompletedAt != nil {
		if r.StartedAt == nil {
			v.add("completed_at", "cannot be set before started_at")
		} else if r.CompletedAt.Before(*r.StartedAt) {
			v.add("completed_at", "must not be before started_at")
		}
	}

	return v.err()
}

// Validate checks an evaluation result before it is written.
func (e *EvaluationResult) Validate() error {
	v := &validator{}

	if e.SimulationRunID == uuid.Nil {
		v.add("simulation_run_id", "is required")
	}
	if e.RootCauseRank != nil && *e.RootCauseRank < 1 {
		v.add("root_cause_rank", "must be positive when set")
	}
	if e.DiagnosisLatencyMS < 0 {
		v.add("diagnosis_latency_ms", "must not be negative")
	}

	for _, m := range []struct {
		field string
		value *float32
	}{
		{"causal_precision", e.CausalPrecision},
		{"causal_recall", e.CausalRecall},
		{"noise_accuracy", e.NoiseAccuracy},
	} {
		if m.value != nil {
			validateConfidence(v, m.field, *m.value)
		}
	}

	return v.err()
}
