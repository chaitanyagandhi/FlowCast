package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockID is the advisory lock every migrating process contends for, so two
// backends starting at once cannot apply the same migration twice. The value is arbitrary
// but must never change.
const migrationLockID int64 = 0x466C6F77 // "Flow"

// migrationFilePattern constrains migration filenames to NNNN_snake_case.sql. Enforcing
// the shape keeps lexicographic filename order identical to numeric version order.
var migrationFilePattern = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// ErrChecksumMismatch reports that an already-applied migration file has been edited.
// Applied migrations are immutable; the fix is a new migration, not a changed one.
var ErrChecksumMismatch = errors.New("migration file changed after it was applied")

// Migration is one SQL file waiting to be, or already, applied.
type Migration struct {
	Version  string // the NNNN prefix, e.g. "0002"
	Filename string
	Checksum string // sha256 of the file contents, hex encoded
	SQL      string
}

// AppliedMigration records a migration this run actually executed.
type AppliedMigration struct {
	Version  string
	Filename string
	Duration time.Duration
}

// Migrate brings the database up to date with the migrations in fsys and returns the ones
// it applied. It is safe to call on every startup: already-applied migrations are skipped,
// and an up-to-date database returns an empty slice.
//
// Concurrent callers serialise on a PostgreSQL advisory lock, so several backend instances
// starting together is not a problem.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, logger *slog.Logger) ([]AppliedMigration, error) {
	available, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}

	// Everything below runs on one connection: the advisory lock is session-scoped.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring connection for migrations: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return nil, fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer func() {
		// Release the lock even when ctx is already cancelled, otherwise it lingers
		// until the connection is reaped and blocks the next startup.
		if _, err := conn.Exec(context.WithoutCancel(ctx),
			"SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
			logger.Error("releasing migration lock", "error", err)
		}
	}()

	if err := ensureMigrationsTable(ctx, conn); err != nil {
		return nil, err
	}

	recorded, err := appliedChecksums(ctx, conn)
	if err != nil {
		return nil, err
	}

	var applied []AppliedMigration
	for _, m := range available {
		if checksum, done := recorded[m.Version]; done {
			if checksum != m.Checksum {
				return nil, fmt.Errorf("%w: %s", ErrChecksumMismatch, m.Filename)
			}
			continue
		}

		took, err := applyMigration(ctx, conn, m)
		if err != nil {
			return nil, err
		}
		logger.Info("applied migration",
			"version", m.Version, "file", m.Filename, "duration", took)
		applied = append(applied, AppliedMigration{
			Version: m.Version, Filename: m.Filename, Duration: took,
		})
	}

	return applied, nil
}

// loadMigrations reads and validates every migration file, in version order.
func loadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(entries)

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[string]string, len(entries))

	for _, name := range entries {
		match := migrationFilePattern.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf(
				"migration %q must be named NNNN_snake_case.sql", name)
		}
		version := match[1]
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf(
				"duplicate migration version %s: %s and %s", version, other, name)
		}
		seen[version] = name

		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("reading migration %s: %w", name, err)
		}

		sum := sha256.Sum256(content)
		migrations = append(migrations, Migration{
			Version:  version,
			Filename: name,
			Checksum: hex.EncodeToString(sum[:]),
			SQL:      string(content),
		})
	}

	return migrations, nil
}

func ensureMigrationsTable(ctx context.Context, c *pgxpool.Conn) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     text        PRIMARY KEY,
    filename    text        NOT NULL,
    checksum    text        NOT NULL,
    duration_ms integer     NOT NULL,
    applied_at  timestamptz NOT NULL DEFAULT now()
)`
	if _, err := c.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}
	return nil
}

// appliedChecksums returns version -> checksum for everything already applied.
func appliedChecksums(ctx context.Context, c *pgxpool.Conn) (map[string]string, error) {
	rows, err := c.Query(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("reading applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]string)
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scanning applied migration: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading applied migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one file and records it, atomically.
//
// The SQL body goes through the simple query protocol because a migration file holds many
// statements; the extended protocol pgx uses for parameterised queries accepts only one.
// Wrapping it in an explicit transaction means a failure halfway through a file leaves
// nothing behind, including the schema_migrations row.
func applyMigration(ctx context.Context, c *pgxpool.Conn, m Migration) (time.Duration, error) {
	pgConn := c.Conn().PgConn()
	start := time.Now()

	if _, err := pgConn.Exec(ctx, "BEGIN").ReadAll(); err != nil {
		return 0, fmt.Errorf("starting transaction for migration %s: %w", m.Filename, err)
	}

	rollback := func() {
		if _, err := pgConn.Exec(context.WithoutCancel(ctx), "ROLLBACK").ReadAll(); err != nil {
			// Nothing useful left to do; the connection is discarded on release.
			_ = err
		}
	}

	if _, err := pgConn.Exec(ctx, m.SQL).ReadAll(); err != nil {
		rollback()
		return 0, fmt.Errorf("applying migration %s: %w", m.Filename, err)
	}

	took := time.Since(start)

	const insert = `
INSERT INTO schema_migrations (version, filename, checksum, duration_ms)
VALUES ($1, $2, $3, $4)`
	if _, err := c.Exec(ctx, insert, m.Version, m.Filename, m.Checksum, took.Milliseconds()); err != nil {
		rollback()
		return 0, fmt.Errorf("recording migration %s: %w", m.Filename, err)
	}

	if _, err := pgConn.Exec(ctx, "COMMIT").ReadAll(); err != nil {
		rollback()
		return 0, fmt.Errorf("committing migration %s: %w", m.Filename, err)
	}

	return took, nil
}
