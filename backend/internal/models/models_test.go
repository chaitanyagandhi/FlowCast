package models_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
)

// --- Enumerations ---

func TestEnumsAcceptTheirOwnValuesAndRejectOthers(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		for _, v := range models.AllProviders() {
			require.True(t, v.Valid(), "%s should be valid", v)
			got, err := models.ParseProvider(string(v))
			require.NoError(t, err)
			require.Equal(t, v, got)
		}
		_, err := models.ParseProvider("opsgenie")
		require.Error(t, err)
	})

	t.Run("severity", func(t *testing.T) {
		for _, v := range models.AllSeverities() {
			require.True(t, v.Valid())
		}
		_, err := models.ParseSeverity("SEV1")
		require.Error(t, err)
	})

	t.Run("incident status", func(t *testing.T) {
		for _, v := range models.AllIncidentStatuses() {
			require.True(t, v.Valid())
		}
		_, err := models.ParseIncidentStatus("closed")
		require.Error(t, err)
	})

	t.Run("source", func(t *testing.T) {
		for _, v := range models.AllSources() {
			require.True(t, v.Valid())
		}
		_, err := models.ParseSource("carrier-pigeon")
		require.Error(t, err)
	})

	t.Run("classification", func(t *testing.T) {
		for _, v := range models.AllClassifications() {
			require.True(t, v.Valid())
		}
		// Lowercase is not the same token: classifications are upper snake case.
		_, err := models.ParseClassification("root_cause")
		require.Error(t, err)
	})

	t.Run("run status", func(t *testing.T) {
		for _, v := range models.AllRunStatuses() {
			require.True(t, v.Valid())
		}
		_, err := models.ParseRunStatus("halfway")
		require.Error(t, err)
	})

	t.Run("fault action", func(t *testing.T) {
		for _, v := range models.AllFaultActions() {
			require.True(t, v.Valid())
		}
		_, err := models.ParseFaultAction("RM_RF_SLASH")
		require.Error(t, err)
	})
}

// The rejection message has to be useful to an API caller, so it names the alternatives.
func TestInvalidEnumErrorListsAllowedValues(t *testing.T) {
	_, err := models.ParseSeverity("SEV1")
	require.Error(t, err)

	var enumErr *models.InvalidEnumError
	require.ErrorAs(t, err, &enumErr)
	require.Equal(t, "severity", enumErr.Kind)
	require.Equal(t, "SEV1", enumErr.Value)
	require.Equal(t, []string{"P1", "P2", "P3", "P4"}, enumErr.Allowed)
	require.Contains(t, err.Error(), "P1, P2, P3, P4")
}

// Causal precision and recall are measured over exactly the causal classifications, so
// which ones count is worth pinning down.
func TestOnlyCauseClassificationsAreCausal(t *testing.T) {
	causal := map[models.Classification]bool{
		models.ClassRootCause:          true,
		models.ClassContributingFactor: true,
	}
	for _, c := range models.AllClassifications() {
		require.Equal(t, causal[c], c.IsCausal(), "%s", c)
	}
}

func TestSourceForProviderMapsCleanly(t *testing.T) {
	for _, p := range models.AllProviders() {
		source := models.SourceForProvider(p)
		require.True(t, source.Valid(), "provider %s must map to a valid source", p)
		require.Equal(t, string(p), string(source))
	}
}

func TestRunStatusTerminality(t *testing.T) {
	require.True(t, models.RunCompleted.Terminal())
	require.True(t, models.RunFailed.Terminal())
	require.False(t, models.RunPending.Terminal())
	require.False(t, models.RunRunning.Terminal())
	require.False(t, models.RunAnalyzing.Terminal())
}

// --- Secrets must not serialize ---

// A hash or webhook secret reaching a response body is a real incident, so this is
// asserted against the actual JSON rather than trusted to review.
func TestSecretsAreNeverSerialized(t *testing.T) {
	t.Run("user password hash", func(t *testing.T) {
		user := models.User{
			ID:           uuid.New(),
			Email:        "ada@example.com",
			Name:         "Ada",
			PasswordHash: "$2a$12$notarealhashbutlooksliketone",
		}
		encoded, err := json.Marshal(user)
		require.NoError(t, err)

		require.NotContains(t, string(encoded), user.PasswordHash)
		require.NotContains(t, string(encoded), "password")
		require.Contains(t, string(encoded), "ada@example.com")
	})

	t.Run("integration webhook secret", func(t *testing.T) {
		integration := models.Integration{
			ID:            uuid.New(),
			Provider:      models.ProviderPagerDuty,
			WebhookSecret: "super-secret-signing-key",
			Enabled:       true,
		}
		encoded, err := json.Marshal(integration)
		require.NoError(t, err)

		require.NotContains(t, string(encoded), integration.WebhookSecret)
		require.Contains(t, string(encoded), "pagerduty")
	})

	t.Run("scenario root cause and run ground truth", func(t *testing.T) {
		scenario := models.SimulationScenario{
			Name:      "database-pool-exhaustion",
			RootCause: "database connection pool exhaustion",
		}
		encoded, err := json.Marshal(scenario)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), scenario.RootCause,
			"ground truth must not leak through the scenario list")

		run := models.SimulationRun{
			Status:      models.RunRunning,
			GroundTruth: models.GroundTruth{RootCause: "bad deployment"},
		}
		encoded, err = json.Marshal(run)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "bad deployment",
			"ground truth must not leak while a run is in flight")
	})
}

func TestIncidentEmbeddingIsNotSerialized(t *testing.T) {
	incident := models.Incident{
		Title:     "Checkout latency",
		Embedding: []float32{0.1, 0.2, 0.3},
	}
	encoded, err := json.Marshal(incident)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "embedding")
	require.NotContains(t, string(encoded), "0.2")
}

func TestJSONUsesSnakeCaseFieldNames(t *testing.T) {
	started := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	incident := models.Incident{
		ID:        uuid.New(),
		TeamID:    uuid.New(),
		Title:     "Checkout latency",
		Severity:  models.SeverityP1,
		Status:    models.StatusOpen,
		Source:    models.SourcePagerDuty,
		StartedAt: started,
	}

	encoded, err := json.Marshal(incident)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	for _, field := range []string{
		"id", "team_id", "title", "severity", "status", "source", "started_at",
	} {
		require.Contains(t, decoded, field)
	}
	// Absent optional fields stay out of the payload rather than appearing as null.
	require.NotContains(t, decoded, "resolved_at")
	require.NotContains(t, decoded, "external_id")
}

// --- Behaviour ---

func TestIncidentDuration(t *testing.T) {
	started := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)
	incident := models.Incident{StartedAt: started, Status: models.StatusOpen}

	_, resolved := incident.Duration()
	require.False(t, resolved, "an open incident has no duration yet")
	require.True(t, incident.IsOpen())

	end := started.Add(42 * time.Minute)
	incident.ResolvedAt = &end
	incident.Status = models.StatusResolved

	duration, resolved := incident.Duration()
	require.True(t, resolved)
	require.Equal(t, 42*time.Minute, duration)
	require.False(t, incident.IsOpen())
}

// The AI stages need a stable event order: the same incident must build the same prompt
// every time, or results between runs are not comparable.
func TestSortEventsByTimeIsDeterministic(t *testing.T) {
	base := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	tied1 := models.Event{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000aa"), OccurredAt: base}
	tied2 := models.Event{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000bb"), OccurredAt: base}
	later := models.Event{ID: uuid.New(), OccurredAt: base.Add(time.Minute)}
	earlier := models.Event{ID: uuid.New(), OccurredAt: base.Add(-time.Minute)}

	events := []models.Event{later, tied2, earlier, tied1}
	models.SortEventsByTime(events)

	require.Equal(t, []uuid.UUID{earlier.ID, tied1.ID, tied2.ID, later.ID},
		[]uuid.UUID{events[0].ID, events[1].ID, events[2].ID, events[3].ID},
		"ties must break by id so the order never varies")
}

func TestAnalysisTopHypothesis(t *testing.T) {
	analysis := models.IncidentAnalysis{}
	_, ok := analysis.Top()
	require.False(t, ok, "an analysis with no hypotheses has no top one")

	analysis.Hypotheses = []models.RootCauseHypothesis{
		{Rank: 3, Cause: "third"},
		{Rank: 1, Cause: "first"},
		{Rank: 2, Cause: "second"},
	}

	top, ok := analysis.Top()
	require.True(t, ok)
	require.Equal(t, "first", top.Cause)

	analysis.SortHypotheses()
	require.Equal(t, []int{1, 2, 3},
		[]int{analysis.Hypotheses[0].Rank, analysis.Hypotheses[1].Rank, analysis.Hypotheses[2].Rank})
}

// --- Validation ---

func requireInvalidFields(t *testing.T, err error, fields ...string) {
	t.Helper()
	require.Error(t, err)

	var verr *models.ValidationError
	require.ErrorAs(t, err, &verr)

	got := make([]string, len(verr.Issues))
	for i, issue := range verr.Issues {
		got[i] = issue.Field
	}
	for _, want := range fields {
		require.Contains(t, got, want)
	}
}

func TestIncidentValidation(t *testing.T) {
	valid := func() models.Incident {
		return models.Incident{
			TeamID:    uuid.New(),
			Title:     "Checkout latency",
			Severity:  models.SeverityP1,
			Status:    models.StatusOpen,
			Source:    models.SourcePagerDuty,
			StartedAt: time.Now().UTC(),
		}
	}

	require.NoError(t, (&models.Incident{
		TeamID: uuid.New(), Title: "ok", Severity: models.SeverityP2,
		Status: models.StatusOpen, Source: models.SourceManual,
		StartedAt: time.Now().UTC(),
	}).Validate())

	t.Run("reports every problem at once", func(t *testing.T) {
		empty := models.Incident{}
		requireInvalidFields(t, empty.Validate(),
			"title", "severity", "status", "source", "team_id", "started_at")
	})

	t.Run("resolved before started", func(t *testing.T) {
		incident := valid()
		before := incident.StartedAt.Add(-time.Hour)
		incident.ResolvedAt = &before
		requireInvalidFields(t, incident.Validate(), "resolved_at")
	})

	t.Run("empty external id", func(t *testing.T) {
		incident := valid()
		blank := ""
		incident.ExternalID = &blank
		requireInvalidFields(t, incident.Validate(), "external_id")
	})

	t.Run("title too long", func(t *testing.T) {
		incident := valid()
		incident.Title = string(make([]byte, 501))
		for i := range incident.Title {
			_ = i
		}
		incident.Title = ""
		for range 501 {
			incident.Title += "a"
		}
		requireInvalidFields(t, incident.Validate(), "title")
	})
}

func TestUserValidation(t *testing.T) {
	valid := models.User{
		TeamID: uuid.New(), Email: "ada@example.com",
		Name: "Ada", PasswordHash: "hash",
	}
	require.NoError(t, valid.Validate())

	for _, email := range []string{"not-an-email", "@example.com", "ada@", "a@b", "a@@b.com"} {
		user := valid
		user.Email = email
		requireInvalidFields(t, user.Validate(), "email")
	}

	missing := models.User{}
	requireInvalidFields(t, missing.Validate(), "email", "name", "password_hash", "team_id")
}

func TestIntegrationValidation(t *testing.T) {
	valid := models.Integration{
		TeamID: uuid.New(), Provider: models.ProviderSlack,
		WebhookSecret: "a-sufficiently-long-secret",
	}
	require.NoError(t, valid.Validate())

	short := valid
	short.WebhookSecret = "tooshort"
	requireInvalidFields(t, short.Validate(), "webhook_secret")

	unknown := valid
	unknown.Provider = "opsgenie"
	requireInvalidFields(t, unknown.Validate(), "provider")
}

func TestEventValidation(t *testing.T) {
	valid := models.Event{
		IncidentID: uuid.New(), Source: models.SourceDatadog,
		EventType: "alert.triggered", Title: "High latency",
		OccurredAt: time.Now().UTC(), Classification: models.ClassUnknown,
	}
	require.NoError(t, valid.Validate())

	zeroRank := 0
	bad := valid
	bad.CausalRank = &zeroRank
	requireInvalidFields(t, bad.Validate(), "causal_rank")

	empty := models.Event{}
	requireInvalidFields(t, empty.Validate(),
		"event_type", "title", "source", "classification", "incident_id", "occurred_at")
}

func TestAnalysisValidation(t *testing.T) {
	valid := models.IncidentAnalysis{
		IncidentID: uuid.New(), PredictedRootCause: "Pool exhaustion",
		Confidence: 0.9, Model: "test-model", PromptVersion: "root-cause-v1",
	}
	require.NoError(t, valid.Validate())

	t.Run("confidence must be a probability", func(t *testing.T) {
		for _, c := range []float32{-0.1, 1.1} {
			analysis := valid
			analysis.Confidence = c
			requireInvalidFields(t, analysis.Validate(), "confidence")
		}
	})

	t.Run("at most three hypotheses", func(t *testing.T) {
		analysis := valid
		for rank := 1; rank <= 4; rank++ {
			analysis.Hypotheses = append(analysis.Hypotheses,
				models.RootCauseHypothesis{Rank: rank, Cause: "c", Confidence: 0.5})
		}
		requireInvalidFields(t, analysis.Validate(), "hypotheses")
	})

	t.Run("ranks must be distinct", func(t *testing.T) {
		analysis := valid
		analysis.Hypotheses = []models.RootCauseHypothesis{
			{Rank: 1, Cause: "a", Confidence: 0.5},
			{Rank: 1, Cause: "b", Confidence: 0.4},
		}
		requireInvalidFields(t, analysis.Validate(), "hypotheses")
	})
}

func TestHypothesisValidation(t *testing.T) {
	valid := models.RootCauseHypothesis{Rank: 1, Cause: "Pool exhaustion", Confidence: 0.9}
	require.NoError(t, valid.Validate())

	for _, rank := range []int{0, 4} {
		h := valid
		h.Rank = rank
		requireInvalidFields(t, h.Validate(), "rank")
	}

	withNil := valid
	withNil.EvidenceEventIDs = []uuid.UUID{uuid.New(), uuid.Nil}
	requireInvalidFields(t, withNil.Validate(), "evidence_event_ids")
}

func TestPostmortemValidation(t *testing.T) {
	valid := models.Postmortem{
		IncidentID:       uuid.New(),
		ExecutiveSummary: "Checkout was degraded for 42 minutes.",
	}
	require.NoError(t, valid.Validate())

	// A thin postmortem beats an invented one, but a wholly empty one says nothing.
	requireInvalidFields(t, (&models.Postmortem{IncidentID: uuid.New()}).Validate(),
		"executive_summary")

	withBadItem := valid
	withBadItem.ActionItems = []models.ActionItem{{Owner: "platform"}}
	requireInvalidFields(t, withBadItem.Validate(), "action_items")
}

func TestScenarioValidationIncludesConfigIssues(t *testing.T) {
	valid := models.SimulationScenario{
		Name: "slow-dependency", Category: "dependency",
		RootCause: "dependency latency",
		Config: models.ScenarioConfig{
			Target: "dependency-service", Fault: models.FaultAddLatency,
			Parameters: map[string]any{"latency_ms": 1500},
		},
	}
	require.NoError(t, valid.Validate())

	t.Run("config problems surface with a prefixed field", func(t *testing.T) {
		scenario := valid
		scenario.Config = models.ScenarioConfig{Fault: "DROP_ALL_TABLES"}
		requireInvalidFields(t, scenario.Validate(),
			"scenario_config.target", "scenario_config.fault")
	})

	t.Run("a scenario without ground truth cannot be scored", func(t *testing.T) {
		scenario := valid
		scenario.RootCause = ""
		requireInvalidFields(t, scenario.Validate(), "root_cause")
	})
}

func TestRunValidation(t *testing.T) {
	valid := models.SimulationRun{
		ScenarioID: uuid.New(), TeamID: uuid.New(), Status: models.RunPending,
	}
	require.NoError(t, valid.Validate())

	now := time.Now().UTC()
	t.Run("completed without started", func(t *testing.T) {
		run := valid
		run.CompletedAt = &now
		requireInvalidFields(t, run.Validate(), "completed_at")
	})

	t.Run("completed before started", func(t *testing.T) {
		earlier := now.Add(-time.Hour)
		run := valid
		run.StartedAt = &now
		run.CompletedAt = &earlier
		requireInvalidFields(t, run.Validate(), "completed_at")
	})
}

func TestEvaluationResultValidation(t *testing.T) {
	valid := models.EvaluationResult{
		SimulationRunID: uuid.New(), RootCauseCorrect: true, DiagnosisLatencyMS: 4200,
	}
	require.NoError(t, valid.Validate(), "unmeasurable metrics may stay nil")

	tooHigh := float32(1.4)
	bad := valid
	bad.CausalPrecision = &tooHigh
	requireInvalidFields(t, bad.Validate(), "causal_precision")

	negative := valid
	negative.DiagnosisLatencyMS = -1
	requireInvalidFields(t, negative.Validate(), "diagnosis_latency_ms")

	zeroRank := 0
	badRank := valid
	badRank.RootCauseRank = &zeroRank
	requireInvalidFields(t, badRank.Validate(), "root_cause_rank")
}

func TestValidationErrorMessageNamesFields(t *testing.T) {
	err := (&models.Incident{}).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "validation failed")
	require.Contains(t, err.Error(), "title is required")
}
