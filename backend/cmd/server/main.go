// Command server is the entrypoint for the FlowCast backend.
//
// FlowCast is a modular monolith: configuration, persistence, webhook ingestion, the AI
// analysis pipeline, background workers, and the incident simulator all run in this one
// process, wired together here.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/chaitanyagandhi/flowcast/backend/internal/auth"
	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/db"
	"github.com/chaitanyagandhi/flowcast/backend/internal/handlers"
	"github.com/chaitanyagandhi/flowcast/backend/internal/queue"
	"github.com/chaitanyagandhi/flowcast/backend/internal/repository"
	"github.com/chaitanyagandhi/flowcast/backend/internal/server"
	"github.com/chaitanyagandhi/flowcast/backend/migrations"
)

// version identifies the build. It is overridden at release time via
// -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Cancelled on SIGINT or SIGTERM, which is what starts a graceful shutdown. A second
	// signal restores the default behaviour and kills the process outright, so a hung
	// shutdown never traps an operator.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		// The bootstrap logger is already gone if configuration failed, so report the
		// problem on stderr and exit non-zero.
		fmt.Fprintf(os.Stderr, "flowcast: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// Bootstrap logger, used until the configured log level is known.
	logger := newLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	dotEnvPath, err := config.LoadDotEnv()
	switch {
	case err == nil:
		logger.Info("loaded local environment file", "path", dotEnvPath)
	case errors.Is(err, config.ErrNoDotEnv):
		logger.Debug("no .env file found; reading configuration from the environment")
	default:
		return fmt.Errorf("loading .env: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger = newLogger(cfg.Server.LogLevel)
	slog.SetDefault(logger)

	logger.Info("flowcast backend starting", "version", version)
	// Safe to log: Config redacts every secret through slog.LogValuer.
	logger.Info("configuration loaded", "config", cfg)

	pool, err := db.Connect(ctx, cfg.Database, logger)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	// Migrations run at startup and serialise on an advisory lock, so starting several
	// instances at once is safe.
	applied, err := db.Migrate(ctx, pool, migrations.FS, logger)
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	logger.Info("database schema up to date", "migrations_applied", len(applied))

	if err := db.VerifyEmbeddingDimensions(ctx, pool, cfg.AI.EmbeddingDimensions); err != nil {
		return err
	}

	redisClient, err := queue.Connect(ctx, cfg.Redis, logger)
	if err != nil {
		return fmt.Errorf("connecting to redis: %w", err)
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			logger.Error("closing redis client", "error", err)
		}
	}()

	hasher, err := auth.NewHasher(cfg.Auth.BcryptCost)
	if err != nil {
		return fmt.Errorf("configuring password hasher: %w", err)
	}
	tokens, err := auth.NewTokens(cfg.Auth)
	if err != nil {
		return fmt.Errorf("configuring tokens: %w", err)
	}

	handler := handlers.NewRouter(handlers.Deps{
		Config:  cfg.Server,
		Logger:  logger,
		Version: version,
		Users:   repository.NewUserRepository(pool),
		Hasher:  hasher,
		Tokens:  tokens,
		// A Secure cookie is not sent over plain http, which local development uses.
		SecureCookies: cfg.Env.IsProduction(),
		// PostgreSQL and Redis are both required for FlowCast to do anything useful,
		// so either being down makes the service unhealthy rather than degraded.
		HealthChecks: []handlers.Check{
			{Name: "postgres", Probe: func(ctx context.Context) error {
				return pool.Ping(ctx)
			}},
			{Name: "redis", Probe: func(ctx context.Context) error {
				return queue.Ping(ctx, redisClient)
			}},
		},
	})

	// Blocks until a signal arrives, then drains in-flight requests.
	if err := server.New(cfg.Server, handler, logger).Run(ctx); err != nil {
		return err
	}

	logger.Info("flowcast backend stopped")
	return nil
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
