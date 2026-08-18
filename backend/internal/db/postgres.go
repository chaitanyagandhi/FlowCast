package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

// applicationName is reported to PostgreSQL so connections from FlowCast are identifiable
// in pg_stat_activity.
const applicationName = "flowcast-backend"

// readyRetryInterval is how long to wait between startup connectivity attempts. The whole
// retry loop is bounded by DatabaseConfig.ConnectTimeout, not by an attempt count.
const readyRetryInterval = 500 * time.Millisecond

// healthCheckPeriod is how often pgx prunes and health-checks idle connections.
const healthCheckPeriod = time.Minute

// Connect builds the PostgreSQL connection pool and verifies the database is actually
// reachable before returning, so a misconfigured or unreachable database is a startup
// failure rather than an error on the first request.
//
// The caller owns the returned pool and must Close it.
func Connect(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating database pool: %w", err)
	}

	if err := waitForReady(ctx, pool, cfg.ConnectTimeout, logger); err != nil {
		pool.Close()
		return nil, err
	}

	logger.Info("connected to postgres",
		"host", poolCfg.ConnConfig.Host,
		"port", poolCfg.ConnConfig.Port,
		"database", poolCfg.ConnConfig.Database,
		"max_conns", poolCfg.MaxConns,
		"min_conns", poolCfg.MinConns,
	)
	return pool, nil
}

// poolConfig derives a pgx pool configuration from application configuration.
func poolConfig(cfg config.DatabaseConfig) (*pgxpool.Config, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		// pgx error text can echo the DSN, which carries the password. Report the
		// failure without it.
		return nil, fmt.Errorf("parsing FLOWCAST_DATABASE_URL: invalid connection string")
	}

	if cfg.MaxConns < 1 || cfg.MaxConns > maxPoolSize {
		return nil, fmt.Errorf("max conns must be between 1 and %d, got %d", maxPoolSize, cfg.MaxConns)
	}
	if cfg.MinConns < 0 || cfg.MinConns > cfg.MaxConns {
		return nil, fmt.Errorf("min conns must be between 0 and max conns (%d), got %d",
			cfg.MaxConns, cfg.MinConns)
	}

	poolCfg.MaxConns = int32(cfg.MaxConns)
	poolCfg.MinConns = int32(cfg.MinConns)
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = healthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["application_name"] = applicationName
	// Every session reads and writes UTC, so a server or client with a local timezone
	// cannot shift stored timestamps.
	poolCfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	return poolCfg, nil
}

// maxPoolSize mirrors the ceiling enforced during configuration validation. It is repeated
// here because Connect must stay safe when called with a hand-built DatabaseConfig, and
// because the value is widened to an int32 for pgx.
const maxPoolSize = 1000

// waitForReady pings until the database answers or the timeout expires. Retrying matters
// under docker compose, where the backend can start while PostgreSQL is still booting.
func waitForReady(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		err := pool.Ping(ctx)
		if err == nil {
			if attempt > 1 {
				logger.Info("database became ready", "attempts", attempt)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("database not reachable within %s (%d attempts): %w",
				timeout, attempt, err)
		case <-time.After(readyRetryInterval):
			logger.Warn("database not ready, retrying",
				"attempt", attempt, "error", err.Error())
		}
	}
}
