package repository_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/db"
	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
	"github.com/chaitanyagandhi/flowcast/backend/internal/repository"
	"github.com/chaitanyagandhi/flowcast/backend/migrations"
)

const testDatabaseURLEnv = "FLOWCAST_TEST_DATABASE_URL"

// migratedSchema gives each test its own migrated PostgreSQL schema, so tests cannot see
// each other's rows and the unique constraints mean what they say.
func migratedSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()

	rawURL := os.Getenv(testDatabaseURLEnv)
	if rawURL == "" {
		t.Skipf("set %s to run repository integration tests", testDatabaseURLEnv)
	}

	ctx := context.Background()
	schema := "test_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))

	admin, err := pgxpool.New(ctx, rawURL)
	require.NoError(t, err)
	defer admin.Close()

	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	require.NoError(t, err)
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), rawURL)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	q := parsed.Query()
	q.Set("search_path", schema+",public")
	parsed.RawQuery = q.Encode()

	logger := slog.New(slog.DiscardHandler)
	pool, err := db.Connect(ctx, config.DatabaseConfig{
		URL:             parsed.String(),
		ConnectTimeout:  5 * time.Second,
		MaxConns:        4,
		MinConns:        1,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	}, logger)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = db.Migrate(ctx, pool, migrations.FS, logger)
	require.NoError(t, err)

	return pool
}

func newUser(email string) models.User {
	return models.User{Email: email, PasswordHash: "$2a$04$notarealhash", Name: "Ada Lovelace"}
}

func TestCreateTeamWithOwnerPersistsBoth(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))
	ctx := context.Background()

	team, user, err := repo.CreateTeamWithOwner(ctx, "Platform", newUser("ada@example.com"))
	require.NoError(t, err)

	require.NotEqual(t, uuid.Nil, team.ID)
	require.Equal(t, "Platform", team.Name)
	require.NotEqual(t, uuid.Nil, user.ID)
	require.Equal(t, team.ID, user.TeamID, "the user must belong to the created team")
	require.False(t, user.CreatedAt.IsZero())

	found, err := repo.FindByEmail(ctx, "ada@example.com")
	require.NoError(t, err)
	require.Equal(t, user.ID, found.ID)
}

// Addresses are stored folded, so lookups never have to remember to fold them.
func TestEmailIsStoredNormalized(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))
	ctx := context.Background()

	_, user, err := repo.CreateTeamWithOwner(ctx, "Platform", newUser("  ADA@Example.COM  "))
	require.NoError(t, err)
	require.Equal(t, "ada@example.com", user.Email)

	for _, variant := range []string{"ada@example.com", "ADA@EXAMPLE.COM", " Ada@Example.com "} {
		found, err := repo.FindByEmail(ctx, variant)
		require.NoError(t, err, "lookup by %q should succeed", variant)
		require.Equal(t, user.ID, found.ID)
	}
}

func TestDuplicateEmailIsAConflict(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))
	ctx := context.Background()

	_, _, err := repo.CreateTeamWithOwner(ctx, "Platform", newUser("ada@example.com"))
	require.NoError(t, err)

	_, _, err = repo.CreateTeamWithOwner(ctx, "Payments", newUser("ADA@example.com"))
	require.ErrorIs(t, err, models.ErrConflict,
		"the same address under a different team is still a duplicate")
}

// Registration writes two rows. A failure on the second must not leave the first behind.
func TestFailedRegistrationLeavesNoOrphanTeam(t *testing.T) {
	pool := migratedSchema(t)
	repo := repository.NewUserRepository(pool)
	ctx := context.Background()

	_, _, err := repo.CreateTeamWithOwner(ctx, "Platform", newUser("ada@example.com"))
	require.NoError(t, err)

	// This attempt fails on the duplicate user, after its team row was inserted.
	_, _, err = repo.CreateTeamWithOwner(ctx, "Orphan Team", newUser("ada@example.com"))
	require.ErrorIs(t, err, models.ErrConflict)

	var orphans int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM teams WHERE name = 'Orphan Team'").Scan(&orphans))
	require.Zero(t, orphans, "the team must be rolled back with the failed user insert")

	var teams int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM teams").Scan(&teams))
	require.Equal(t, 1, teams)
}

func TestFindByEmailReportsMissingUser(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))

	_, err := repo.FindByEmail(context.Background(), "nobody@example.com")
	require.ErrorIs(t, err, models.ErrNotFound)
}

func TestFindByIDRoundTrips(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))
	ctx := context.Background()

	_, user, err := repo.CreateTeamWithOwner(ctx, "Platform", newUser("ada@example.com"))
	require.NoError(t, err)

	found, err := repo.FindByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.Email, found.Email)
	require.Equal(t, user.TeamID, found.TeamID)

	_, err = repo.FindByID(ctx, uuid.New())
	require.ErrorIs(t, err, models.ErrNotFound)
}

func TestFindTeamByID(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))
	ctx := context.Background()

	team, _, err := repo.CreateTeamWithOwner(ctx, "Platform", newUser("ada@example.com"))
	require.NoError(t, err)

	found, err := repo.FindTeamByID(ctx, team.ID)
	require.NoError(t, err)
	require.Equal(t, "Platform", found.Name)

	_, err = repo.FindTeamByID(ctx, uuid.New())
	require.ErrorIs(t, err, models.ErrNotFound)
}

// The stored hash must be exactly what was handed in: the repository is not in the
// business of transforming credentials.
func TestPasswordHashIsStoredVerbatim(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))
	ctx := context.Background()

	user := newUser("ada@example.com")
	user.PasswordHash = "$2a$04$abcdefghijklmnopqrstuv"

	_, created, err := repo.CreateTeamWithOwner(ctx, "Platform", user)
	require.NoError(t, err)
	require.Equal(t, user.PasswordHash, created.PasswordHash)

	found, err := repo.FindByEmail(ctx, "ada@example.com")
	require.NoError(t, err)
	require.Equal(t, user.PasswordHash, found.PasswordHash)
}

func TestDifferentTeamsCanEachHaveMembers(t *testing.T) {
	repo := repository.NewUserRepository(migratedSchema(t))
	ctx := context.Background()

	for i, email := range []string{"ada@example.com", "grace@example.com"} {
		team, user, err := repo.CreateTeamWithOwner(ctx,
			fmt.Sprintf("Team %d", i), newUser(email))
		require.NoError(t, err)
		require.Equal(t, team.ID, user.TeamID)
	}
}
