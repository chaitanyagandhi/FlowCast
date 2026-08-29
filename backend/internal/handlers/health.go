package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
)

// DefaultHealthTimeout bounds every dependency probe. A health endpoint that hangs is
// worse than one that reports a failure: the thing polling it usually has its own,
// shorter, patience.
const DefaultHealthTimeout = 2 * time.Second

// Check is one named dependency probe.
type Check struct {
	Name  string
	Probe func(ctx context.Context) error
}

// Status values reported by the endpoint.
const (
	statusOK          = "ok"
	statusDegraded    = "degraded"
	statusUnavailable = "unavailable"
)

// healthResponse is the body of GET /health.
type healthResponse struct {
	Status  string                 `json:"status"`
	Version string                 `json:"version"`
	Checks  map[string]checkResult `json:"checks"`
}

// checkResult reports a dependency's state and how long it took to answer.
//
// It deliberately carries no error text. The endpoint is the most reachable thing in the
// system, and a driver error routinely names hosts, databases, and connection strings.
// The cause goes to the logs, where it belongs.
type checkResult struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
}

type healthHandler struct {
	checks  []Check
	version string
	timeout time.Duration
	logger  *slog.Logger
}

// Health returns a handler reporting whether FlowCast's dependencies are usable.
//
// Probes run concurrently and are individually bounded, so one wedged dependency neither
// hides the others nor holds the response open.
func Health(version string, timeout time.Duration, logger *slog.Logger, checks ...Check) http.Handler {
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	return &healthHandler{checks: checks, version: version, timeout: timeout, logger: logger}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	results := make([]checkResult, len(h.checks))

	var wg sync.WaitGroup
	for i, check := range h.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine writes its own slot, so no lock is needed.
			results[i] = h.run(ctx, check, r)
		}()
	}
	wg.Wait()

	body := healthResponse{
		Status:  statusOK,
		Version: h.version,
		Checks:  make(map[string]checkResult, len(h.checks)),
	}
	for i, check := range h.checks {
		body.Checks[check.Name] = results[i]
		if results[i].Status != statusOK {
			body.Status = statusDegraded
		}
	}

	// 503 rather than 200-with-a-sad-body: load balancers and probes read the status
	// line, not the payload.
	status := http.StatusOK
	if body.Status != statusOK {
		status = http.StatusServiceUnavailable
	}
	api.WriteJSON(w, r, status, body)
}

// run executes one probe, recovering from a panic so a broken check degrades that
// dependency rather than taking down the endpoint reporting on it.
func (h *healthHandler) run(ctx context.Context, check Check, r *http.Request) (result checkResult) {
	started := time.Now()

	defer func() {
		if recovered := recover(); recovered != nil {
			h.logger.ErrorContext(ctx, "health check panicked",
				"check", check.Name,
				"panic", recovered,
				"request_id", api.RequestID(r.Context()))
			result = checkResult{
				Status:    statusUnavailable,
				LatencyMS: time.Since(started).Milliseconds(),
			}
		}
	}()

	err := check.Probe(ctx)
	latency := time.Since(started).Milliseconds()

	if err != nil {
		h.logger.WarnContext(ctx, "health check failed",
			"check", check.Name,
			"error", err,
			"latency_ms", latency,
			"request_id", api.RequestID(r.Context()))
		return checkResult{Status: statusUnavailable, LatencyMS: latency}
	}

	return checkResult{Status: statusOK, LatencyMS: latency}
}
