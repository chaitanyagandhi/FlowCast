package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/migrations"
)

// migratedSchema returns a pool pointing at a freshly migrated, test-private schema.
func migratedSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newTestSchema(t)
	_, err := Migrate(context.Background(), pool, migrations.FS, discardLogger())
	require.NoError(t, err)
	return pool
}

func insertTeam(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		"INSERT INTO teams (name) VALUES ($1) RETURNING id", name).Scan(&id))
	return id
}

func TestIdentitySchemaCreatesExpectedTables(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()

	for _, table := range []string{"teams", "users", "integrations", "schema_migrations"} {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = current_schema() AND table_name = $1
			)`, table).Scan(&exists))
		require.True(t, exists, "table %s should exist", table)
	}
}

func TestUsersEmailIsUniqueCaseInsensitively(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	teamID := insertTeam(t, pool, "Platform")

	_, err := pool.Exec(ctx, `
		INSERT INTO users (team_id, email, password_hash, name)
		VALUES ($1, 'ada@example.com', 'hash', 'Ada')`, teamID)
	require.NoError(t, err)

	// Same address, different casing, and even a different team: still a duplicate.
	otherTeam := insertTeam(t, pool, "Payments")
	_, err = pool.Exec(ctx, `
		INSERT INTO users (team_id, email, password_hash, name)
		VALUES ($1, 'Ada@Example.com', 'hash', 'Ada again')`, otherTeam)
	require.Error(t, err)
	require.Contains(t, err.Error(), "users_email_lower_key")
}

func TestIntegrationsAreOnePerProviderPerTeam(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	teamA := insertTeam(t, pool, "Team A")
	teamB := insertTeam(t, pool, "Team B")

	insert := `
		INSERT INTO integrations (team_id, provider, webhook_secret)
		VALUES ($1, $2, 'secret-at-least-16-chars')`

	_, err := pool.Exec(ctx, insert, teamA, "pagerduty")
	require.NoError(t, err)

	// A second team may use the same provider.
	_, err = pool.Exec(ctx, insert, teamB, "pagerduty")
	require.NoError(t, err)

	// The same team may not configure it twice.
	_, err = pool.Exec(ctx, insert, teamA, "pagerduty")
	require.Error(t, err)
	require.Contains(t, err.Error(), "integrations_team_provider_key")
}

func TestIntegrationsRejectUnknownProvider(t *testing.T) {
	pool := migratedSchema(t)
	teamID := insertTeam(t, pool, "Platform")

	_, err := pool.Exec(context.Background(), `
		INSERT INTO integrations (team_id, provider, webhook_secret)
		VALUES ($1, 'opsgenie', 'secret-at-least-16-chars')`, teamID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "integrations_provider_check")
}

// Deleting a team must not strand its users or integrations.
func TestDeletingTeamCascades(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	teamID := insertTeam(t, pool, "Doomed")

	_, err := pool.Exec(ctx, `
		INSERT INTO users (team_id, email, password_hash, name)
		VALUES ($1, 'someone@example.com', 'hash', 'Someone')`, teamID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO integrations (team_id, provider, webhook_secret)
		VALUES ($1, 'slack', 'secret-at-least-16-chars')`, teamID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, "DELETE FROM teams WHERE id = $1", teamID)
	require.NoError(t, err)

	for _, table := range []string{"users", "integrations"} {
		var remaining int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT count(*) FROM "+table+" WHERE team_id = $1", teamID).Scan(&remaining))
		require.Zero(t, remaining, "%s rows should be removed with the team", table)
	}
}

// updated_at is maintained by a trigger, so it stays correct even for a statement run by
// hand against the database.
func TestUpdatedAtTriggerFires(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()
	teamID := insertTeam(t, pool, "Original")

	var created, updated time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT created_at, updated_at FROM teams WHERE id = $1", teamID).
		Scan(&created, &updated))
	require.WithinDuration(t, created, updated, time.Second)

	_, err := pool.Exec(ctx, "UPDATE teams SET name = 'Renamed' WHERE id = $1", teamID)
	require.NoError(t, err)

	var createdAfter, updatedAfter time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT created_at, updated_at FROM teams WHERE id = $1", teamID).
		Scan(&createdAfter, &updatedAfter))

	require.True(t, updatedAfter.After(updated), "updated_at should advance")
	require.Equal(t, created, createdAfter, "created_at must not change")
}

// Every timestamp column must be timestamptz. A naive `timestamp` silently drops the zone
// and is the usual way an incident timeline ends up hours out.
//
// Note this is about storage, not rendering: timestamptz always stores an absolute UTC
// instant, while pgx decodes it into the process's local zone. The instant is what has to
// survive the round trip.
func TestTimestampColumnsAreTimezoneAware(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND (column_name LIKE '%_at' OR data_type LIKE 'timestamp%')
		ORDER BY table_name, column_name`)
	require.NoError(t, err)
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var table, column, dataType string
		require.NoError(t, rows.Scan(&table, &column, &dataType))
		require.Equal(t, "timestamp with time zone", dataType,
			"%s.%s must be timestamptz", table, column)
		checked++
	}
	require.NoError(t, rows.Err())
	require.GreaterOrEqual(t, checked, 7, "expected the created_at/updated_at columns")
}

// A specific instant must survive the round trip unchanged, and server-side now() must
// agree with the client's clock -- either would break if a timezone were being applied
// twice somewhere.
func TestTimestampsRoundTripAsAbsoluteInstants(t *testing.T) {
	pool := migratedSchema(t)
	ctx := context.Background()

	// Deliberately not UTC: the zone should be irrelevant to what gets stored.
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	want := time.Date(2026, 3, 14, 9, 26, 53, 0, tokyo)

	var got time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT $1::timestamptz", want).Scan(&got))
	require.True(t, want.Equal(got), "want %s, got %s", want, got)

	var serverNow time.Time
	require.NoError(t, pool.QueryRow(ctx, "SELECT now()").Scan(&serverNow))
	require.WithinDuration(t, time.Now(), serverNow, 5*time.Second)
}

func TestBlankNamesAreRejected(t *testing.T) {
	pool := migratedSchema(t)

	_, err := pool.Exec(context.Background(), "INSERT INTO teams (name) VALUES ('   ')")
	require.Error(t, err)
	require.Contains(t, err.Error(), "teams_name_check")
}
