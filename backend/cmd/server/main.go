// Command server is the entrypoint for the FlowCast backend.
//
// FlowCast is a modular monolith: configuration, persistence, webhook ingestion, the AI
// analysis pipeline, background workers, and the incident simulator all run in this one
// process, wired together here.
package main

import (
	"log/slog"
	"os"
)

// version identifies the build. It is overridden at release time via
// -ldflags "-X main.version=...".
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("flowcast backend starting", "version", version)

	// Configuration, database, Redis, HTTP server, and workers are wired in here as the
	// corresponding packages under internal/ are implemented.
	logger.Info("flowcast backend scaffold ready; no services wired yet")
}
