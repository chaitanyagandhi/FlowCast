package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func insertScenario(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO simulation_scenarios (name, category, root_cause, scenario_config)
		VALUES ($1, 'database', 'database connection pool exhaustion',
		        '{"target":"api-service","fault":"EXHAUST_DB_POOL"}'::jsonb)
		RETURNING id`, name).Scan(&id))
	return id
}

func insertRun(t *testing.T, pool *pgxpool.Pool, scenarioID, teamID string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO simulation_runs (scenario_id, team_id)
		VALUES ($1, $2) RETURNING id`, scenarioID, teamID).Scan(&id))
	return id
}

func TestSimulationSchemaCreatesExpectedTables(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()

	for _, table := range []string{
		"simulation_scenarios", "simulation_runs", "evaluation_results",
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

func TestScenarioNamesAreUnique(t *testing.T) {
	pool := migratedSchema(t)
	insertScenario(t, pool, "database-pool-exhaustion")

	_, err := pool.Exec(context.Background(), `
		INSERT INTO simulation_scenarios (name, category, root_cause)
		VALUES ('database-pool-exhaustion', 'database', 'something else')`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "simulation_scenarios_name_key")
}

// A scenario without a stated root cause cannot be scored, so it is not a scenario.
func TestScenarioRequiresRootCause(t *testing.T) {
	pool := migratedSchema(t)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO simulation_scenarios (name, category, root_cause)
		VALUES ('nameless', 'database', '   ')`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "simulation_scenarios_root_cause_check")
}

func TestRunsStartPendingWithoutIncident(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")

	runID := insertRun(t, pool, scenarioID, teamID)

	var status string
	var incidentID, startedAt, completedAt *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT status, incident_id, started_at::text, completed_at::text
		FROM simulation_runs WHERE id = $1`, runID).
		Scan(&status, &incidentID, &startedAt, &completedAt))

	require.Equal(t, "pending", status)
	require.Nil(t, incidentID, "a queued run has no incident yet")
	require.Nil(t, startedAt)
	require.Nil(t, completedAt)
}

func TestRunRejectsUnknownStatus(t *testing.T) {
	pool := migratedSchema(t)
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")

	_, err := pool.Exec(context.Background(), `
		INSERT INTO simulation_runs (scenario_id, team_id, status)
		VALUES ($1, $2, 'halfway')`, scenarioID, teamID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "simulation_runs_status_check")
}

func TestRunCannotCompleteBeforeItStarts(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")

	// Completed with no start at all.
	_, err := pool.Exec(ctx, `
		INSERT INTO simulation_runs (scenario_id, team_id, completed_at)
		VALUES ($1, $2, now())`, scenarioID, teamID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "simulation_runs_completed_after_start")

	// Completed before it started.
	_, err = pool.Exec(ctx, `
		INSERT INTO simulation_runs (scenario_id, team_id, started_at, completed_at)
		VALUES ($1, $2, now(), now() - interval '5 minutes')`, scenarioID, teamID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "simulation_runs_completed_after_start")
}

// The experimental record outlives the catalogue entry: a scenario with runs against it
// cannot simply be deleted.
func TestScenarioWithRunsCannotBeDeleted(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "bad-deployment")
	teamID := insertTeam(t, pool, "Platform")
	insertRun(t, pool, scenarioID, teamID)

	_, err := pool.Exec(ctx, "DELETE FROM simulation_scenarios WHERE id = $1", scenarioID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "simulation_runs_scenario_id_fkey")
}

// Deleting the generated incident must not destroy the run or its score.
func TestDeletingIncidentKeepsRunAndClearsLink(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "queue-backlog")
	teamID := insertTeam(t, pool, "Platform")
	incidentID := insertIncident(t, pool, teamID)

	var runID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO simulation_runs (scenario_id, team_id, incident_id, status)
		VALUES ($1, $2, $3, 'completed') RETURNING id`,
		scenarioID, teamID, incidentID).Scan(&runID))

	_, err := pool.Exec(ctx, "DELETE FROM incidents WHERE id = $1", incidentID)
	require.NoError(t, err)

	var stillThere bool
	var linkedIncident *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT true, incident_id FROM simulation_runs WHERE id = $1`, runID).
		Scan(&stillThere, &linkedIncident))

	require.True(t, stillThere, "the run is the experimental record and must survive")
	require.Nil(t, linkedIncident)
}

func TestDeletingTeamRemovesItsRuns(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Doomed")
	insertRun(t, pool, scenarioID, teamID)

	_, err := pool.Exec(ctx, "DELETE FROM teams WHERE id = $1", teamID)
	require.NoError(t, err)

	var remaining int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM simulation_runs").Scan(&remaining))
	require.Zero(t, remaining)
}

func TestEvaluationResultIsOnePerRun(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")
	runID := insertRun(t, pool, scenarioID, teamID)

	insert := `
		INSERT INTO evaluation_results
			(simulation_run_id, root_cause_correct, diagnosis_latency_ms)
		VALUES ($1, true, 4200)`

	_, err := pool.Exec(ctx, insert, runID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, insert, runID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "evaluation_results_simulation_run_id_key")
}

// Metrics that are not measurable for a run stay NULL rather than being recorded as zero,
// which would quietly drag an average down.
func TestUnmeasurableMetricsStayNull(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")
	runID := insertRun(t, pool, scenarioID, teamID)

	var precision, recall, noise *float32
	var rank *int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO evaluation_results
			(simulation_run_id, root_cause_correct, diagnosis_latency_ms)
		VALUES ($1, false, 1500)
		RETURNING causal_precision, causal_recall, noise_accuracy, root_cause_rank`, runID).
		Scan(&precision, &recall, &noise, &rank))

	require.Nil(t, precision)
	require.Nil(t, recall)
	require.Nil(t, noise)
	require.Nil(t, rank, "no rank means the true cause was not in the hypotheses at all")
}

func TestEvaluationMetricsMustBeProbabilities(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")

	for _, column := range []string{"causal_precision", "causal_recall", "noise_accuracy"} {
		t.Run(column, func(t *testing.T) {
			runID := insertRun(t, pool, scenarioID, teamID)
			_, err := pool.Exec(ctx, `
				INSERT INTO evaluation_results
					(simulation_run_id, root_cause_correct, diagnosis_latency_ms, `+column+`)
				VALUES ($1, true, 100, 1.4)`, runID)
			require.Error(t, err)
			require.Contains(t, err.Error(), "evaluation_results_"+column+"_check")
		})
	}
}

func TestDiagnosisLatencyCannotBeNegative(t *testing.T) {
	pool := migratedSchema(t)
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")
	runID := insertRun(t, pool, scenarioID, teamID)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO evaluation_results
			(simulation_run_id, root_cause_correct, diagnosis_latency_ms)
		VALUES ($1, true, -1)`, runID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "evaluation_results_diagnosis_latency_ms_check")
}

func TestDeletingRunRemovesItsEvaluation(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "slow-dependency")
	teamID := insertTeam(t, pool, "Platform")
	runID := insertRun(t, pool, scenarioID, teamID)

	_, err := pool.Exec(ctx, `
		INSERT INTO evaluation_results
			(simulation_run_id, root_cause_correct, diagnosis_latency_ms)
		VALUES ($1, true, 900)`, runID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, "DELETE FROM simulation_runs WHERE id = $1", runID)
	require.NoError(t, err)

	var remaining int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM evaluation_results").Scan(&remaining))
	require.Zero(t, remaining)
}

// Ground truth is snapshotted onto the run, so editing the scenario afterwards cannot
// retroactively change what an already-scored run was judged against.
func TestRunGroundTruthIsIndependentOfLaterScenarioEdits(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	scenarioID := insertScenario(t, pool, "bad-deployment")
	teamID := insertTeam(t, pool, "Platform")

	var runID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO simulation_runs (scenario_id, team_id, ground_truth)
		VALUES ($1, $2, '{"root_cause":"bad deployment"}'::jsonb)
		RETURNING id`, scenarioID, teamID).Scan(&runID))

	_, err := pool.Exec(ctx, `
		UPDATE simulation_scenarios SET root_cause = 'rewritten history' WHERE id = $1`,
		scenarioID)
	require.NoError(t, err)

	var groundTruth string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT ground_truth->>'root_cause' FROM simulation_runs WHERE id = $1", runID).
		Scan(&groundTruth))
	require.Equal(t, "bad deployment", groundTruth)
}

// Ground truth must be reachable only from the simulation tables. Nothing the analysis
// pipeline reads -- incidents and events -- may carry it, or a passing score proves
// nothing. This pins the schema-level half of that separation; the query-level half is
// enforced and tested with the evaluation pipeline.
func TestGroundTruthIsConfinedToSimulationTables(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND (column_name LIKE '%ground_truth%' OR column_name LIKE '%root_cause%')
		ORDER BY table_name, column_name`)
	require.NoError(t, err)
	defer rows.Close()

	found := map[string][]string{}
	for rows.Next() {
		var table, column string
		require.NoError(t, rows.Scan(&table, &column))
		found[table] = append(found[table], column)
	}
	require.NoError(t, rows.Err())

	require.Equal(t, map[string][]string{
		// Ground truth.
		"simulation_scenarios": {"root_cause"},
		"simulation_runs":      {"ground_truth"},
		// The pipeline's own output, not ground truth. incident_analyses names its
		// column predicted_root_cause precisely so the two cannot be confused.
		"incident_analyses": {"predicted_root_cause"},
		"postmortems":       {"root_cause"},
		// Scores derived by comparing the two, written only after analysis finished.
		"evaluation_results": {"root_cause_correct", "root_cause_rank"},
	}, found)

	// Nothing the analysis pipeline reads may carry ground truth.
	for _, table := range []string{"incidents", "events"} {
		require.NotContains(t, found, table,
			"%s is read during analysis and must not carry ground truth", table)
	}
}
