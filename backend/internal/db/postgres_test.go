package db

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

// testDatabaseURLEnv points the integration tests at a live PostgreSQL instance. When it
// is unset those tests skip, so `go test ./...` still passes on a machine with no database.
const testDatabaseURLEnv = "FLOWCAST_TEST_DATABASE_URL"

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func validConfig(url string) config.DatabaseConfig {
	return config.DatabaseConfig{
		URL:             url,
		ConnectTimeout:  5 * time.Second,
		MaxConns:        8,
		MinConns:        2,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	}
}

func TestPoolConfigAppliesSettings(t *testing.T) {
	cfg := validConfig("postgres://user:pw@db.example:5432/flowcast?sslmode=disable")

	poolCfg, err := poolConfig(cfg)
	require.NoError(t, err)

	require.Equal(t, int32(8), poolCfg.MaxConns)
	require.Equal(t, int32(2), poolCfg.MinConns)
	require.Equal(t, time.Hour, poolCfg.MaxConnLifetime)
	require.Equal(t, 30*time.Minute, poolCfg.MaxConnIdleTime)
	require.Equal(t, healthCheckPeriod, poolCfg.HealthCheckPeriod)

	require.Equal(t, "db.example", poolCfg.ConnConfig.Host)
	require.Equal(t, uint16(5432), poolCfg.ConnConfig.Port)
	require.Equal(t, "flowcast", poolCfg.ConnConfig.Database)
	require.Equal(t, 5*time.Second, poolCfg.ConnConfig.ConnectTimeout)

	require.Equal(t, applicationName, poolCfg.ConnConfig.RuntimeParams["application_name"])
	require.Equal(t, "UTC", poolCfg.ConnConfig.RuntimeParams["timezone"])
}

func TestPoolConfigRejectsInvalidURL(t *testing.T) {
	cfg := validConfig("://not a dsn")

	_, err := poolConfig(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "FLOWCAST_DATABASE_URL")
}

// A parse failure must not echo the connection string back, because it contains the
// database password and the error is logged at startup.
func TestPoolConfigErrorDoesNotLeakPassword(t *testing.T) {
	cfg := validConfig("postgres://user:hunter2-secret@:::bad-port/flowcast")

	_, err := poolConfig(cfg)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2-secret")
}

func TestPoolConfigRejectsBadPoolSizes(t *testing.T) {
	tests := []struct {
		name     string
		maxConns int
		minConns int
		wantText string
	}{
		{"zero max", 0, 0, "max conns"},
		{"max above ceiling", maxPoolSize + 1, 1, "max conns"},
		{"negative min", 5, -1, "min conns"},
		{"min above max", 5, 6, "min conns"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig("postgres://user:pw@localhost:5432/flowcast")
			cfg.MaxConns = tc.maxConns
			cfg.MinConns = tc.minConns

			_, err := poolConfig(cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantText)
		})
	}
}

// An unreachable database is a startup failure, not a lazily discovered one.
func TestConnectFailsWhenDatabaseUnreachable(t *testing.T) {
	cfg := validConfig("postgres://user:pw@127.0.0.1:1/flowcast?sslmode=disable")
	cfg.ConnectTimeout = 900 * time.Millisecond

	start := time.Now()
	pool, err := Connect(context.Background(), cfg, discardLogger())

	require.Error(t, err)
	require.Nil(t, pool, "the pool must be closed and not handed back on failure")
	require.Contains(t, err.Error(), "not reachable")
	// It retried for roughly the timeout rather than giving up on the first refusal.
	require.GreaterOrEqual(t, time.Since(start), 500*time.Millisecond)
}

// A caller's cancelled context must abort the retry loop promptly.
func TestConnectHonoursCallerContext(t *testing.T) {
	cfg := validConfig("postgres://user:pw@127.0.0.1:1/flowcast?sslmode=disable")
	cfg.ConnectTimeout = time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Connect(ctx, cfg, discardLogger())

	require.Error(t, err)
	require.Less(t, time.Since(start), 10*time.Second, "should not wait out ConnectTimeout")
}

// --- Integration tests: require a live PostgreSQL instance ---

func integrationConfig(t *testing.T) config.DatabaseConfig {
	t.Helper()
	url := os.Getenv(testDatabaseURLEnv)
	if url == "" {
		t.Skipf("set %s to run database integration tests", testDatabaseURLEnv)
	}
	return validConfig(url)
}

func TestConnectAgainstLiveDatabase(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	pool, err := Connect(ctx, cfg, discardLogger())
	require.NoError(t, err)
	defer pool.Close()

	var one int
	require.NoError(t, pool.QueryRow(ctx, "select 1").Scan(&one))
	require.Equal(t, 1, one)
}

// The session settings in poolConfig are only useful if PostgreSQL actually receives them.
func TestLiveConnectionAppliesSessionSettings(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	pool, err := Connect(ctx, cfg, discardLogger())
	require.NoError(t, err)
	defer pool.Close()

	var appName, timezone string
	require.NoError(t, pool.QueryRow(ctx, "show application_name").Scan(&appName))
	require.NoError(t, pool.QueryRow(ctx, "show timezone").Scan(&timezone))

	require.Equal(t, applicationName, appName)
	require.Equal(t, "UTC", timezone)
}

func TestLivePoolRespectsConfiguredSize(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	pool, err := Connect(ctx, cfg, discardLogger())
	require.NoError(t, err)
	defer pool.Close()

	require.Equal(t, int32(cfg.MaxConns), pool.Stat().MaxConns())
	require.NoError(t, pool.Ping(ctx))
}
