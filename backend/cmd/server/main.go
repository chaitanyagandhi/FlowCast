// Command server is the entrypoint for the FlowCast backend.
//
// FlowCast is a modular monolith: configuration, persistence, webhook ingestion, the AI
// analysis pipeline, background workers, and the incident simulator all run in this one
// process, wired together here.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

// version identifies the build. It is overridden at release time via
// -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		// The bootstrap logger is already gone if configuration failed, so report the
		// problem on stderr and exit non-zero.
		fmt.Fprintf(os.Stderr, "flowcast: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
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

	// Database, Redis, HTTP server, and workers are wired in here as the corresponding
	// packages under internal/ are implemented.
	logger.Info("flowcast backend scaffold ready; no services wired yet")
	return nil
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
