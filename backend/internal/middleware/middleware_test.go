package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/middleware"
)

// captureLogs returns a logger writing JSON into buf, so assertions can be made about the
// records actually emitted rather than about the code that emits them.
func captureLogs() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, &buf
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record), "log line: %s", line)
		records = append(records, record)
	}
	return records
}

func serve(handler http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	return rec
}

// --- RequestID ---

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	var seen string
	handler := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = api.RequestID(r.Context())
	}))

	rec := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	require.NotEmpty(t, seen)
	_, err := uuid.Parse(seen)
	require.NoError(t, err, "a generated id should be a uuid")
	require.Equal(t, seen, rec.Header().Get(api.RequestIDHeader),
		"the id must be echoed so a user can quote it")
}

func TestRequestIDHonoursValidInboundHeader(t *testing.T) {
	var seen string
	handler := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = api.RequestID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(api.RequestIDHeader, "upstream-trace-42")

	rec := serve(handler, req)

	require.Equal(t, "upstream-trace-42", seen, "a trace should survive a hop between services")
	require.Equal(t, "upstream-trace-42", rec.Header().Get(api.RequestIDHeader))
}

// A caller-supplied id lands in every log line for the request, so a hostile one must be
// replaced rather than adopted.
func TestRequestIDReplacesUnsafeInboundHeader(t *testing.T) {
	hostile := map[string]string{
		"log injection": `x","level":"ERROR","msg":"database deleted`,
		"newline":       "abc\ndef",
		"oversized":     strings.Repeat("a", api.MaxRequestIDLen+1),
	}

	for name, value := range hostile {
		t.Run(name, func(t *testing.T) {
			var seen string
			handler := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = api.RequestID(r.Context())
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(api.RequestIDHeader, value)
			serve(handler, req)

			require.NotEqual(t, value, seen)
			_, err := uuid.Parse(seen)
			require.NoError(t, err, "the hostile value should be replaced by a fresh uuid")
		})
	}
}

// --- Logger ---

func TestLoggerRecordsRequestDetails(t *testing.T) {
	logger, buf := captureLogs()

	handler := middleware.RequestID(middleware.Logger(logger)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})))

	serve(handler, httptest.NewRequest(http.MethodPost, "/api/v1/incidents", nil))

	records := logRecords(t, buf)
	require.Len(t, records, 1)

	record := records[0]
	require.Equal(t, "http request", record["msg"])
	require.Equal(t, "POST", record["method"])
	require.Equal(t, "/api/v1/incidents", record["path"])
	require.Equal(t, float64(http.StatusCreated), record["status"])
	require.Equal(t, float64(11), record["bytes"])
	require.NotEmpty(t, record["request_id"])
	require.Contains(t, record, "duration_ms")
	require.Contains(t, record, "remote_ip")
}

// A handler that writes a body without setting a status has implicitly sent 200.
func TestLoggerDefaultsToOKWhenHandlerNeverSetsStatus(t *testing.T) {
	logger, buf := captureLogs()

	handler := middleware.Logger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))

	serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, float64(http.StatusOK), logRecords(t, buf)[0]["status"])
}

// Filtering logs to warn and above should surface exactly the failed requests.
func TestLoggerLevelFollowsOutcome(t *testing.T) {
	tests := []struct {
		status int
		level  string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusNotFound, "WARN"},
		{http.StatusUnprocessableEntity, "WARN"},
		{http.StatusInternalServerError, "ERROR"},
		{http.StatusServiceUnavailable, "ERROR"},
	}

	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			logger, buf := captureLogs()
			handler := middleware.Logger(logger)(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
				}))

			serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))
			require.Equal(t, tc.level, logRecords(t, buf)[0]["level"])
		})
	}
}

// The path is logged; the query string is not, because it carries identifiers and tokens.
func TestLoggerOmitsQueryString(t *testing.T) {
	logger, buf := captureLogs()

	handler := middleware.Logger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	serve(handler, httptest.NewRequest(http.MethodGet, "/api/v1/incidents?token=super-secret", nil))

	record := logRecords(t, buf)[0]
	require.Equal(t, "/api/v1/incidents", record["path"])
	require.NotContains(t, buf.String(), "super-secret")
}

// --- Recoverer ---

func TestRecovererTurnsPanicIntoJSONError(t *testing.T) {
	logger, buf := captureLogs()

	handler := middleware.RequestID(middleware.Recoverer(logger)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("connection string postgres://user:hunter2@db/flowcast is invalid")
		})))

	rec := serve(handler, httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.CodeInternal, body.Error)
	require.NotEmpty(t, body.RequestID, "the caller needs something to quote")

	// The panic detail belongs in the log, never in the response.
	require.NotContains(t, rec.Body.String(), "hunter2")
	require.NotContains(t, rec.Body.String(), "goroutine")

	record := logRecords(t, buf)[0]
	require.Equal(t, "panic recovered", record["msg"])
	require.Contains(t, record["panic"], "hunter2")
	require.Contains(t, record["stack"], "goroutine")
}

// http.ErrAbortHandler is how net/http deliberately abandons a response. Swallowing it
// would turn an intentional abort into a bogus 500.
func TestRecovererRepanicsOnAbortHandler(t *testing.T) {
	logger, _ := captureLogs()

	handler := middleware.Recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

// Once a status line is out the response is committed; appending an error body would
// corrupt it, so only the log entry remains.
func TestRecovererDoesNotOverwriteACommittedResponse(t *testing.T) {
	logger, buf := captureLogs()

	// Logger wraps the writer in the recorder that tracks whether a write happened.
	handler := middleware.Logger(logger)(middleware.Recoverer(logger)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"partial":true`))
			panic("failed halfway through streaming")
		})))

	rec := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code, "the committed status must stand")
	require.Equal(t, `{"partial":true`, rec.Body.String(),
		"no error body should be appended to a committed response")

	var sawPanic bool
	for _, record := range logRecords(t, buf) {
		if record["msg"] == "panic recovered" {
			sawPanic = true
		}
	}
	require.True(t, sawPanic, "the panic must still be recorded")
}

// The logger sits outside the recoverer so a recovered panic is logged as the 500 it
// became, not as a successful request.
func TestPanicIsLoggedAsAServerError(t *testing.T) {
	logger, buf := captureLogs()

	handler := middleware.RequestID(middleware.Logger(logger)(middleware.Recoverer(logger)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))))

	serve(handler, httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil))

	records := logRecords(t, buf)
	require.Len(t, records, 2, "expected a panic record and a request record")

	request := records[1]
	require.Equal(t, "http request", request["msg"])
	require.Equal(t, float64(http.StatusInternalServerError), request["status"])
	require.Equal(t, "ERROR", request["level"])
}

// --- CORS ---

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	handler := middleware.CORS([]string{"http://localhost:3000"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/incidents", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")

	rec := serve(handler, req)

	require.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "POST")
	require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"),
		"the frontend sends its auth cookie")
}

// Expose-Headers is only sent on an actual response, not on a preflight, so the frontend
// can read the request id off a real reply when reporting a failure.
func TestCORSExposesRequestIDOnActualResponses(t *testing.T) {
	handler := middleware.CORS([]string{"http://localhost:3000"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	req.Header.Set("Origin", "http://localhost:3000")

	rec := serve(handler, req)

	require.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Contains(t, rec.Header().Get("Access-Control-Expose-Headers"), "Request-Id")
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	handler := middleware.CORS([]string{"http://localhost:3000"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	req.Header.Set("Origin", "https://evil.example")

	rec := serve(handler, req)
	require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"),
		"an unlisted origin must not be granted access")
}

// A wildcard origin with credentials is rejected by browsers, so credentials are dropped
// rather than producing a configuration that silently fails.
func TestCORSWildcardDisablesCredentials(t *testing.T) {
	handler := middleware.CORS([]string{"*"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	req.Header.Set("Origin", "https://anywhere.example")

	rec := serve(handler, req)
	require.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"))
}
