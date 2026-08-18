// Package server owns the HTTP listener's lifecycle: starting it, and stopping it without
// dropping requests that are already in flight. What it serves is decided elsewhere -- it
// takes a handler and knows nothing about routes.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

// Server runs an HTTP handler and shuts it down gracefully.
type Server struct {
	http            *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// New builds a server for the given handler. It does not bind a port; call Run or Serve.
func New(cfg config.ServerConfig, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		http: &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.Port),
			Handler: handler,

			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			IdleTimeout:  cfg.IdleTimeout,
			// Bounds how long a client may take to send its headers. Without it a
			// trickle of header bytes can hold a connection open indefinitely.
			ReadHeaderTimeout: cfg.ReadTimeout,

			// net/http logs its own protocol-level errors through a *log.Logger.
			// Route them into slog so there is one log format, not two.
			ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
		logger:          logger,
		shutdownTimeout: cfg.ShutdownTimeout,
	}
}

// Run binds the configured port and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.http.Addr, err)
	}
	return s.Serve(ctx, listener)
}

// Serve serves on an existing listener until ctx is cancelled, then drains.
//
// It returns nil on a clean shutdown, and an error if the server failed or if connections
// were still open when the shutdown deadline passed.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	serveResult := make(chan error, 1)

	go func() {
		// ErrServerClosed is the expected result of a deliberate shutdown, not a
		// failure.
		err := s.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()

	s.logger.Info("http server listening", "addr", listener.Addr().String())

	select {
	case err := <-serveResult:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		// Serve returned on its own, without anyone asking it to.
		return nil

	case <-ctx.Done():
		return s.shutdown(serveResult)
	}
}

// shutdown stops accepting connections and waits for in-flight requests to finish.
func (s *Server) shutdown(serveResult <-chan error) error {
	s.logger.Info("shutting down http server", "grace_period", s.shutdownTimeout)

	// A fresh context: the one that triggered shutdown is already cancelled, and the
	// grace period has to outlive it.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		// The deadline passed with requests still running. Drop them rather than
		// hanging: an operator asked this process to stop.
		s.logger.Warn("grace period expired, closing connections",
			"grace_period", s.shutdownTimeout)
		if closeErr := s.http.Close(); closeErr != nil {
			s.logger.Error("closing http server", "error", closeErr)
		}
		<-serveResult
		return fmt.Errorf("graceful shutdown exceeded %s: %w", s.shutdownTimeout, err)
	}

	// Shutdown has returned, so Serve has too.
	if err := <-serveResult; err != nil {
		return fmt.Errorf("http server: %w", err)
	}

	s.logger.Info("http server stopped cleanly")
	return nil
}
