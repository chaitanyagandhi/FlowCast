package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/handlers"
)

type healthBody struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Checks  map[string]struct {
		Status    string `json:"status"`
		LatencyMS int64  `json:"latency_ms"`
	} `json:"checks"`
}

func callHealth(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, healthBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var body healthBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body),
		"health must always answer with JSON, got: %s", rec.Body.String())
	return rec, body
}

func healthyCheck(name string) handlers.Check {
	return handlers.Check{Name: name, Probe: func(context.Context) error { return nil }}
}

func failingCheck(name string, err error) handlers.Check {
	return handlers.Check{Name: name, Probe: func(context.Context) error { return err }}
}

func TestHealthReportsOKWhenEveryDependencyAnswers(t *testing.T) {
	h := handlers.Health("v1.2.3", time.Second, slog.New(slog.DiscardHandler),
		healthyCheck("postgres"), healthyCheck("redis"))

	rec, body := callHealth(t, h)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok", body.Status)
	require.Equal(t, "v1.2.3", body.Version)
	require.Len(t, body.Checks, 2)
	require.Equal(t, "ok", body.Checks["postgres"].Status)
	require.Equal(t, "ok", body.Checks["redis"].Status)
}

// A probe that reports 200 while a dependency is down is worse than no probe at all.
func TestHealthReports503WhenADependencyIsDown(t *testing.T) {
	h := handlers.Health("dev", time.Second, slog.New(slog.DiscardHandler),
		healthyCheck("postgres"),
		failingCheck("redis", errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")))

	rec, body := callHealth(t, h)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"orchestrators read the status line, not the payload")
	require.Equal(t, "degraded", body.Status)
	require.Equal(t, "ok", body.Checks["postgres"].Status,
		"a working dependency should still be reported as working")
	require.Equal(t, "unavailable", body.Checks["redis"].Status)
}

// The endpoint is the most reachable thing in the system, and driver errors name hosts,
// databases, and connection strings.
func TestHealthNeverLeaksTheUnderlyingError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	cause := errors.New(`failed to connect to host=prod-db.internal user=flowcast password=hunter2`)
	h := handlers.Health("dev", time.Second, logger, failingCheck("postgres", cause))

	rec, _ := callHealth(t, h)

	require.NotContains(t, rec.Body.String(), "hunter2")
	require.NotContains(t, rec.Body.String(), "prod-db.internal")
	require.NotContains(t, rec.Body.String(), "password")

	// The operator still gets the detail, through the logs.
	require.Contains(t, logs.String(), "hunter2")
	require.Contains(t, logs.String(), "health check failed")
}

// One wedged dependency must not hold the response open, or whatever is polling gives up
// first and learns nothing.
func TestHealthBoundsAHangingProbe(t *testing.T) {
	released := make(chan struct{})
	defer close(released)

	hanging := handlers.Check{Name: "postgres", Probe: func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-released:
			return nil
		}
	}}

	h := handlers.Health("dev", 100*time.Millisecond, slog.New(slog.DiscardHandler),
		hanging, healthyCheck("redis"))

	started := time.Now()
	rec, body := callHealth(t, h)
	elapsed := time.Since(started)

	require.Less(t, elapsed, 3*time.Second, "the timeout must cut the probe short")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "unavailable", body.Checks["postgres"].Status)
	require.Equal(t, "ok", body.Checks["redis"].Status,
		"a hung dependency must not hide a healthy one")
}

// Probes run concurrently, so total time tracks the slowest rather than their sum.
func TestHealthRunsProbesConcurrently(t *testing.T) {
	const probeDelay = 150 * time.Millisecond

	slow := func(name string) handlers.Check {
		return handlers.Check{Name: name, Probe: func(ctx context.Context) error {
			select {
			case <-time.After(probeDelay):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	}

	h := handlers.Health("dev", 2*time.Second, slog.New(slog.DiscardHandler),
		slow("postgres"), slow("redis"), slow("other"))

	started := time.Now()
	rec, body := callHealth(t, h)
	elapsed := time.Since(started)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, body.Checks, 3)
	require.Less(t, elapsed, 3*probeDelay,
		"three %s probes finishing in %s means they ran in sequence", probeDelay, elapsed)
}

// A broken probe should degrade its own dependency, not take down the endpoint whose job
// is to report on it.
func TestHealthSurvivesAPanickingProbe(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	panicking := handlers.Check{Name: "postgres", Probe: func(context.Context) error {
		panic("nil pool dereferenced")
	}}

	h := handlers.Health("dev", time.Second, logger, panicking, healthyCheck("redis"))

	rec, body := callHealth(t, h)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "unavailable", body.Checks["postgres"].Status)
	require.Equal(t, "ok", body.Checks["redis"].Status)
	require.Contains(t, logs.String(), "health check panicked")
	require.NotContains(t, rec.Body.String(), "nil pool dereferenced")
}

func TestHealthWithNoChecksIsOK(t *testing.T) {
	h := handlers.Health("dev", time.Second, slog.New(slog.DiscardHandler))

	rec, body := callHealth(t, h)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok", body.Status)
	require.Empty(t, body.Checks)
}

func TestHealthProbesRunOncePerRequest(t *testing.T) {
	var calls atomic.Int64
	counting := handlers.Check{Name: "postgres", Probe: func(context.Context) error {
		calls.Add(1)
		return nil
	}}

	h := handlers.Health("dev", time.Second, slog.New(slog.DiscardHandler), counting)

	for range 3 {
		callHealth(t, h)
	}
	require.Equal(t, int64(3), calls.Load(), "results must not be cached between requests")
}

// --- Routing ---

func TestHealthIsRoutedAtRoot(t *testing.T) {
	router := handlers.NewRouter(handlers.Deps{
		Config:       testServerConfig(),
		Logger:       slog.New(slog.DiscardHandler),
		Version:      "wired",
		HealthChecks: []handlers.Check{healthyCheck("postgres"), healthyCheck("redis")},
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var body healthBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "ok", body.Status)
	require.Equal(t, "wired", body.Version)
	require.ElementsMatch(t, []string{"postgres", "redis"}, keysOf(body.Checks))
}

// Health has to answer before a user could possibly be authenticated, so it sits outside
// /api/v1 where the auth middleware will go.
func TestHealthIsNotUnderTheAPIPrefix(t *testing.T) {
	router := handlers.NewRouter(handlers.Deps{
		Config:  testServerConfig(),
		Logger:  slog.New(slog.DiscardHandler),
		Version: "dev",
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHealthRejectsNonGET(t *testing.T) {
	router := handlers.NewRouter(handlers.Deps{
		Config:       testServerConfig(),
		Logger:       slog.New(slog.DiscardHandler),
		Version:      "dev",
		HealthChecks: []handlers.Check{healthyCheck("postgres")},
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Contains(t, rec.Body.String(), "method_not_allowed")
}

// The middleware chain covers /health too: a failing probe should be traceable.
func TestHealthResponseCarriesRequestID(t *testing.T) {
	router := handlers.NewRouter(handlers.Deps{
		Config:       testServerConfig(),
		Logger:       slog.New(slog.DiscardHandler),
		Version:      "dev",
		HealthChecks: []handlers.Check{failingCheck("redis", errors.New("down"))},
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotEmpty(t, rec.Header().Get("X-Request-Id"))
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
