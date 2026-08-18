package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// embeddingDimensions mirrors the vector width fixed in 0003_incidents.sql.
const embeddingDimensions = 1536

func insertIncident(t *testing.T, pool *pgxpool.Pool, teamID string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO incidents (team_id, title, severity, source, started_at)
		VALUES ($1, 'Checkout latency spike', 'P1', 'pagerduty', now())
		RETURNING id`, teamID).Scan(&id))
	return id
}

func insertAnalysis(t *testing.T, pool *pgxpool.Pool, incidentID string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO incident_analyses
			(incident_id, predicted_root_cause, confidence, model, prompt_version)
		VALUES ($1, 'Database connection pool exhaustion', 0.91, 'test-model', 'root-cause-v1')
		RETURNING id`, incidentID).Scan(&id))
	return id
}

func TestIncidentSchemaCreatesExpectedTables(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()

	for _, table := range []string{
		"incidents", "events", "incident_analyses", "root_cause_hypotheses", "postmortems",
	} {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = current_schema() AND table_name = $1
			)`, table).Scan(&exists))
		require.True(t, exists, "table %s should exist", table)
	}
}

func TestIncidentEnumeratedColumnsRejectUnknownValues(t *testing.T) {
	pool := migratedSchema(t)
	teamID := insertTeam(t, pool, "Platform")

	tests := []struct {
		name       string
		severity   string
		status     string
		source     string
		wantConstr string
	}{
		{"bad severity", "SEV1", "open", "manual", "incidents_severity_check"},
		{"bad status", "P1", "closed", "manual", "incidents_status_check"},
		{"bad source", "P1", "open", "opsgenie", "incidents_source_check"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(context.Background(), `
				INSERT INTO incidents (team_id, title, severity, status, source, started_at)
				VALUES ($1, 'Title', $2, $3, $4, now())`,
				teamID, tc.severity, tc.status, tc.source)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantConstr)
		})
	}
}

func TestIncidentExternalIDIsUniquePerTeamAndOptional(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	teamA := insertTeam(t, pool, "Team A")
	teamB := insertTeam(t, pool, "Team B")

	insert := `
		INSERT INTO incidents (team_id, external_id, title, severity, source, started_at)
		VALUES ($1, $2, 'Title', 'P2', 'pagerduty', now())`

	_, err := pool.Exec(ctx, insert, teamA, "PD-123")
	require.NoError(t, err)

	// The same provider id under a different team is a different incident.
	_, err = pool.Exec(ctx, insert, teamB, "PD-123")
	require.NoError(t, err)

	// Re-delivering the same provider id to the same team is a duplicate.
	_, err = pool.Exec(ctx, insert, teamA, "PD-123")
	require.Error(t, err)
	require.Contains(t, err.Error(), "incidents_team_external_id_key")

	// Manually created incidents have no external id, and many may coexist.
	for range 3 {
		_, err = pool.Exec(ctx, insert, teamA, nil)
		require.NoError(t, err)
	}
}

func TestIncidentResolvedAtCannotPrecedeStart(t *testing.T) {
	pool := migratedSchema(t)
	teamID := insertTeam(t, pool, "Platform")

	_, err := pool.Exec(context.Background(), `
		INSERT INTO incidents (team_id, title, severity, source, started_at, resolved_at)
		VALUES ($1, 'Title', 'P3', 'manual', now(), now() - interval '1 hour')`, teamID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "incidents_resolved_after_start")
}

func TestEventsDefaultToUnclassified(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	var classification string
	var causalRank *int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO events (incident_id, source, event_type, title, occurred_at)
		VALUES ($1, 'datadog', 'alert.triggered', 'High latency', now())
		RETURNING classification, causal_rank`, incidentID).
		Scan(&classification, &causalRank))

	// Ingestion records what happened; the AI stage decides what it means.
	require.Equal(t, "UNKNOWN", classification)
	require.Nil(t, causalRank)
}

func TestEventsRejectUnknownClassification(t *testing.T) {
	pool := migratedSchema(t)
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	_, err := pool.Exec(context.Background(), `
		INSERT INTO events (incident_id, source, event_type, title, occurred_at, classification)
		VALUES ($1, 'slack', 'message', 'Looking into it', now(), 'PROBABLY_BAD')`, incidentID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "events_classification_check")
}

func TestEventsPreserveRawPayload(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	raw := `{"event":{"id":"PD-1","urgency":"high"},"nested":{"kept":true}}`
	var stored string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO events (incident_id, source, event_type, title, occurred_at, raw_payload)
		VALUES ($1, 'pagerduty', 'incident.triggered', 'Triggered', now(), $2::jsonb)
		RETURNING raw_payload::text`, incidentID, raw).Scan(&stored))

	require.Contains(t, stored, `"urgency": "high"`)
	require.Contains(t, stored, `"kept": true`)
}

func TestAnalysisConfidenceMustBeAProbability(t *testing.T) {
	pool := migratedSchema(t)
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	for _, confidence := range []float64{-0.1, 1.5} {
		_, err := pool.Exec(context.Background(), `
			INSERT INTO incident_analyses
				(incident_id, predicted_root_cause, confidence, model, prompt_version)
			VALUES ($1, 'Cause', $2, 'm', 'v1')`, incidentID, confidence)
		require.Error(t, err, "confidence %v should be rejected", confidence)
		require.Contains(t, err.Error(), "incident_analyses_confidence_check")
	}
}

// Re-analysing an incident adds a row rather than replacing one, which is what makes
// model and prompt comparisons possible later.
func TestAnalysesAccumulatePerIncident(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	for _, version := range []string{"root-cause-v1", "root-cause-v2"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO incident_analyses
				(incident_id, predicted_root_cause, confidence, model, prompt_version)
			VALUES ($1, 'Cause', 0.8, 'test-model', $2)`, incidentID, version)
		require.NoError(t, err)
	}

	var count int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM incident_analyses WHERE incident_id = $1", incidentID).Scan(&count))
	require.Equal(t, 2, count)
}

func TestHypothesesAreRankedOneToThreeWithoutTies(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	analysisID := insertAnalysis(t, pool,
		insertIncident(t, pool, insertTeam(t, pool, "Platform")))

	insert := `
		INSERT INTO root_cause_hypotheses (analysis_id, rank, cause, confidence)
		VALUES ($1, $2, 'Some cause', 0.5)`

	for rank := 1; rank <= 3; rank++ {
		_, err := pool.Exec(ctx, insert, analysisID, rank)
		require.NoError(t, err)
	}

	// At most three hypotheses.
	_, err := pool.Exec(ctx, insert, analysisID, 4)
	require.Error(t, err)
	require.Contains(t, err.Error(), "root_cause_hypotheses_rank_check")

	// And no two share a rank.
	_, err = pool.Exec(ctx, insert, analysisID, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "root_cause_hypotheses_analysis_rank_key")
}

func TestHypothesisEvidenceIDsRoundTrip(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))
	analysisID := insertAnalysis(t, pool, incidentID)

	var eventID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO events (incident_id, source, event_type, title, occurred_at)
		VALUES ($1, 'datadog', 'alert.triggered', 'DB errors', now())
		RETURNING id`, incidentID).Scan(&eventID))

	var stored []string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO root_cause_hypotheses (analysis_id, rank, cause, confidence, evidence_event_ids)
		VALUES ($1, 1, 'Pool exhaustion', 0.9, $2)
		RETURNING evidence_event_ids`, analysisID, []string{eventID}).Scan(&stored))

	require.Equal(t, []string{eventID}, stored)
}

func TestPostmortemIsOnePerIncident(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	insert := `INSERT INTO postmortems (incident_id, executive_summary) VALUES ($1, 'Summary')`

	_, err := pool.Exec(ctx, insert, incidentID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, insert, incidentID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "postmortems_incident_id_key")
}

func TestPostmortemListFieldsMustBeJSONArrays(t *testing.T) {
	pool := migratedSchema(t)
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	_, err := pool.Exec(context.Background(), `
		INSERT INTO postmortems (incident_id, action_items)
		VALUES ($1, '{"not":"an array"}'::jsonb)`, incidentID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "postmortems_action_items_check")
}

// Deleting an incident must take its whole analysis trail with it, including hypotheses
// reached only through their analysis.
func TestDeletingIncidentCascadesToAllChildren(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))
	analysisID := insertAnalysis(t, pool, incidentID)

	_, err := pool.Exec(ctx, `
		INSERT INTO events (incident_id, source, event_type, title, occurred_at)
		VALUES ($1, 'datadog', 'alert.triggered', 'Alert', now())`, incidentID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO root_cause_hypotheses (analysis_id, rank, cause, confidence)
		VALUES ($1, 1, 'Cause', 0.9)`, analysisID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO postmortems (incident_id) VALUES ($1)`, incidentID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, "DELETE FROM incidents WHERE id = $1", incidentID)
	require.NoError(t, err)

	for _, table := range []string{
		"events", "incident_analyses", "root_cause_hypotheses", "postmortems",
	} {
		var remaining int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT count(*) FROM "+table).Scan(&remaining))
		require.Zero(t, remaining, "%s should be emptied with the incident", table)
	}
}

func TestDeletingTeamRemovesItsIncidents(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	teamID := insertTeam(t, pool, "Doomed")
	insertIncident(t, pool, teamID)

	_, err := pool.Exec(ctx, "DELETE FROM teams WHERE id = $1", teamID)
	require.NoError(t, err)

	var remaining int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM incidents").Scan(&remaining))
	require.Zero(t, remaining)
}

// --- pgvector ---

// fixedVector builds a constant-width vector literal for tests.
func fixedVector(value float64) string {
	return fmt.Sprintf("array_fill(%v::real, ARRAY[%d])::vector", value, embeddingDimensions)
}

func TestEmbeddingsStoreAndRankBySimilarity(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	teamID := insertTeam(t, pool, "Platform")

	insert := fmt.Sprintf(`
		INSERT INTO incidents (team_id, title, severity, source, started_at, embedding)
		VALUES ($1, $2, 'P2', 'manual', now(), %s)
		RETURNING id`, "%s")

	var nearID, farID string
	require.NoError(t, pool.QueryRow(ctx,
		fmt.Sprintf(insert, fixedVector(0.1)), teamID, "near").Scan(&nearID))
	require.NoError(t, pool.QueryRow(ctx,
		fmt.Sprintf(insert, fixedVector(-0.1)), teamID, "far").Scan(&farID))

	// Cosine distance against a query vector pointing the same way as "near".
	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT id FROM incidents
		WHERE team_id = $1 AND embedding IS NOT NULL
		ORDER BY embedding <=> %s
		LIMIT 2`, fixedVector(0.1)), teamID)
	require.NoError(t, err)
	defer rows.Close()

	var ordered []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ordered = append(ordered, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{nearID, farID}, ordered)
}

func TestEmbeddingRejectsWrongWidth(t *testing.T) {
	pool := migratedSchema(t)
	teamID := insertTeam(t, pool, "Platform")

	_, err := pool.Exec(context.Background(), `
		INSERT INTO incidents (team_id, title, severity, source, started_at, embedding)
		VALUES ($1, 'Title', 'P2', 'manual', now(), array_fill(0.1::real, ARRAY[3])::vector)`,
		teamID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected 1536 dimensions")
}

func TestVerifyEmbeddingDimensions(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()

	require.NoError(t, VerifyEmbeddingDimensions(ctx, pool, embeddingDimensions))

	err := VerifyEmbeddingDimensions(ctx, pool, 768)
	require.ErrorIs(t, err, ErrEmbeddingDimensionMismatch)
	require.Contains(t, err.Error(), "vector(1536)")
	require.Contains(t, err.Error(), "768")
}

func TestVerifyEmbeddingDimensionsReportsMissingSchema(t *testing.T) {
	// No migrations applied, and public off the search path so the check cannot resolve
	// some other database's incidents table by accident.
	pool := newTestSchemaWithoutPublic(t)

	err := VerifyEmbeddingDimensions(context.Background(), pool, embeddingDimensions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "migrations")
}

func TestIncidentTimestampsUseTriggerAndUTC(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	incidentID := insertIncident(t, pool, insertTeam(t, pool, "Platform"))

	var before time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT updated_at FROM incidents WHERE id = $1", incidentID).Scan(&before))

	_, err := pool.Exec(ctx,
		"UPDATE incidents SET status = 'resolved' WHERE id = $1", incidentID)
	require.NoError(t, err)

	var after time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT updated_at FROM incidents WHERE id = $1", incidentID).Scan(&after))
	require.True(t, after.After(before))
}
