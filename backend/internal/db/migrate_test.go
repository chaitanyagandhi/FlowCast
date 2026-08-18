package db

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/migrations"
)

// --- Unit tests: no database required ---

func TestLoadMigrationsOrdersByVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"0010_later.sql":   {Data: []byte("select 10;")},
		"0002_middle.sql":  {Data: []byte("select 2;")},
		"0001_first.sql":   {Data: []byte("select 1;")},
		"notes.md":         {Data: []byte("ignored")},
		"0003_another.sql": {Data: []byte("select 3;")},
	}

	got, err := loadMigrations(fsys)
	require.NoError(t, err)

	versions := make([]string, len(got))
	for i, m := range got {
		versions[i] = m.Version
	}
	require.Equal(t, []string{"0001", "0002", "0003", "0010"}, versions)
	// Non-SQL files are not migrations.
	require.Len(t, got, 4)
}

func TestLoadMigrationsRejectsBadFilenames(t *testing.T) {
	tests := map[string]string{
		"missing number prefix": "create_users.sql",
		"too few digits":        "001_users.sql",
		"uppercase in name":     "0001_Users.sql",
		"spaces in name":        "0001_add users.sql",
	}

	for name, filename := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadMigrations(fstest.MapFS{filename: {Data: []byte("select 1;")}})
			require.Error(t, err)
			require.Contains(t, err.Error(), "NNNN_snake_case.sql")
		})
	}
}

func TestLoadMigrationsRejectsDuplicateVersions(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_teams.sql": {Data: []byte("select 1;")},
		"0001_users.sql": {Data: []byte("select 2;")},
	}

	_, err := loadMigrations(fsys)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate migration version 0001")
}

func TestLoadMigrationsChecksumsContent(t *testing.T) {
	same1, err := loadMigrations(fstest.MapFS{"0001_a.sql": {Data: []byte("select 1;")}})
	require.NoError(t, err)
	same2, err := loadMigrations(fstest.MapFS{"0001_a.sql": {Data: []byte("select 1;")}})
	require.NoError(t, err)
	diff, err := loadMigrations(fstest.MapFS{"0001_a.sql": {Data: []byte("select 2;")}})
	require.NoError(t, err)

	require.Equal(t, same1[0].Checksum, same2[0].Checksum)
	require.NotEqual(t, same1[0].Checksum, diff[0].Checksum)
	require.Len(t, same1[0].Checksum, 64, "sha256 hex digest")
}

// The embedded migrations must be loadable, so a malformed filename fails the build's
// tests rather than a deployment.
func TestEmbeddedMigrationsAreValid(t *testing.T) {
	got, err := loadMigrations(migrations.FS)
	require.NoError(t, err)
	require.NotEmpty(t, got)

	for _, m := range got {
		require.NotEmpty(t, strings.TrimSpace(m.SQL), "%s is empty", m.Filename)
	}
}

// --- Integration tests: require a live PostgreSQL instance ---

// newTestSchema gives a test its own PostgreSQL schema and a pool whose search_path points
// at it, so migrations from different tests cannot collide. The schema is dropped when the
// test finishes.
func newTestSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg := integrationConfig(t) // skips when FLOWCAST_TEST_DATABASE_URL is unset
	ctx := context.Background()

	schema := fmt.Sprintf("test_%s", strings.ToLower(randomSuffix(t)))

	admin, err := pgxpool.New(ctx, cfg.URL)
	require.NoError(t, err)
	defer admin.Close()

	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), cfg.URL)
		if err != nil {
			t.Logf("cleanup: %v", err)
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.Exec(context.Background(),
			"DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Logf("dropping schema %s: %v", schema, err)
		}
	})

	// public stays on the path so the extensions installed there resolve.
	scoped := cfg
	scoped.URL = withSearchPath(t, cfg.URL, schema+",public")

	pool, err := Connect(ctx, scoped, discardLogger())
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool
}

func withSearchPath(t *testing.T, raw, searchPath string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", searchPath)
	u.RawQuery = q.Encode()
	return u.String()
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	// Test names are unique within a package and already sanitised by Go.
	return strings.NewReplacer("/", "_", " ", "_", "-", "_").Replace(t.Name())
}

func TestMigrateAppliesEverythingThenIsIdempotent(t *testing.T) {
	pool := newTestSchema(t)
	ctx := context.Background()

	applied, err := Migrate(ctx, pool, migrations.FS, discardLogger())
	require.NoError(t, err)
	require.NotEmpty(t, applied)

	versions := make([]string, len(applied))
	for i, a := range applied {
		versions[i] = a.Version
	}
	require.Equal(t, []string{"0001", "0002"}, versions)

	// A second run has nothing to do.
	again, err := Migrate(ctx, pool, migrations.FS, discardLogger())
	require.NoError(t, err)
	require.Empty(t, again)
}

func TestMigrateRecordsMetadata(t *testing.T) {
	pool := newTestSchema(t)
	ctx := context.Background()

	_, err := Migrate(ctx, pool, migrations.FS, discardLogger())
	require.NoError(t, err)

	var version, filename, checksum string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT version, filename, checksum FROM schema_migrations
		WHERE version = '0002'`).Scan(&version, &filename, &checksum))

	require.Equal(t, "0002", version)
	require.Equal(t, "0002_identity.sql", filename)
	require.Len(t, checksum, 64)
}

// Editing a migration that has already run is a mistake worth stopping the process for.
func TestMigrateDetectsEditedMigration(t *testing.T) {
	pool := newTestSchema(t)
	ctx := context.Background()

	original := fstest.MapFS{
		"0001_thing.sql": {Data: []byte("CREATE TABLE thing (id int primary key);")},
	}
	_, err := Migrate(ctx, pool, original, discardLogger())
	require.NoError(t, err)

	edited := fstest.MapFS{
		"0001_thing.sql": {Data: []byte("CREATE TABLE thing (id bigint primary key);")},
	}
	_, err = Migrate(ctx, pool, edited, discardLogger())
	require.ErrorIs(t, err, ErrChecksumMismatch)
	require.Contains(t, err.Error(), "0001_thing.sql")
}

// A failing migration must leave neither its schema changes nor its bookkeeping row.
func TestMigrateRollsBackFailedMigration(t *testing.T) {
	pool := newTestSchema(t)
	ctx := context.Background()

	broken := fstest.MapFS{
		"0001_broken.sql": {Data: []byte(`
			CREATE TABLE first_table (id int primary key);
			THIS IS NOT SQL;
		`)},
	}

	_, err := Migrate(ctx, pool, broken, discardLogger())
	require.Error(t, err)
	require.Contains(t, err.Error(), "0001_broken.sql")

	var tables int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'first_table'`).Scan(&tables))
	require.Zero(t, tables, "the earlier statement in the file must be rolled back")

	var recorded int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&recorded))
	require.Zero(t, recorded, "a failed migration must not be recorded as applied")
}

// Two backends starting at once must not both apply the same migration.
func TestMigrateIsSafeUnderConcurrency(t *testing.T) {
	pool := newTestSchema(t)
	ctx := context.Background()

	const racers = 4
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		total   int
		failure error
	)

	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()
			applied, err := Migrate(ctx, pool, migrations.FS, discardLogger())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failure = err
			}
			total += len(applied)
		}()
	}
	wg.Wait()

	require.NoError(t, failure)
	require.Equal(t, 2, total, "each migration must be applied exactly once in total")
}
