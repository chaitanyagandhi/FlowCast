package handlers_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/handlers"
)

// testServerConfig is the server configuration shared by the router and health tests.
func testServerConfig() config.ServerConfig {
	return config.ServerConfig{
		Port:            8080,
		ShutdownTimeout: time.Second,
		CORSOrigins:     []string{"http://localhost:3000"},
	}
}

func testRouter(t *testing.T) http.Handler {
	t.Helper()
	return handlers.NewRouter(handlers.Deps{
		Config:  testServerConfig(),
		Logger:  slog.New(slog.DiscardHandler),
		Version: "test",
	})
}

func do(t *testing.T, router http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) api.ErrorResponse {
	t.Helper()
	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body),
		"every response must be JSON, got: %s", rec.Body.String())
	return body
}

// chi answers an unknown route with plain text by default. Every FlowCast response is
// JSON, including the ones nobody planned for.
func TestUnknownRouteReturnsJSONNotFound(t *testing.T) {
	rec := do(t, testRouter(t), httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	body := decodeError(t, rec)
	require.Equal(t, api.CodeNotFound, body.Error)
	require.NotEmpty(t, body.Message)
	require.NotEmpty(t, body.RequestID, "an error must be traceable back to a log line")
}

// The request id in the body and the one in the header have to be the same value, or
// quoting either is useless.
func TestErrorRequestIDMatchesResponseHeader(t *testing.T) {
	rec := do(t, testRouter(t), httptest.NewRequest(http.MethodGet, "/missing", nil))

	require.Equal(t, rec.Header().Get(api.RequestIDHeader), decodeError(t, rec).RequestID)
}

func TestInboundRequestIDFlowsThroughToTheErrorBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set(api.RequestIDHeader, "trace-from-gateway")

	rec := do(t, testRouter(t), req)

	require.Equal(t, "trace-from-gateway", decodeError(t, rec).RequestID)
	require.Equal(t, "trace-from-gateway", rec.Header().Get(api.RequestIDHeader))
}

// This router has no routes registered yet, which is exactly the case chi gets wrong:
// its Use chain is skipped entirely when the mux is empty, so the whole stack would go
// silently missing. NewRouter wraps the mux instead, and this test holds that line.
func TestMiddlewareRunsOnEveryResponseIncludingNotFound(t *testing.T) {
	rec := do(t, testRouter(t), httptest.NewRequest(http.MethodGet, "/nothing-here", nil))

	require.NotEmpty(t, rec.Header().Get(api.RequestIDHeader),
		"request id middleware must cover unmatched routes too")
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

func TestPreflightIsAnsweredForAllowedOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/incidents", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")

	rec := do(t, testRouter(t), req)

	require.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Less(t, rec.Code, 400,
		"a preflight must be answered by CORS rather than falling through to 404")
}

func TestPreflightFromUnknownOriginGetsNoGrant(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/incidents", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")

	rec := do(t, testRouter(t), req)
	require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}
