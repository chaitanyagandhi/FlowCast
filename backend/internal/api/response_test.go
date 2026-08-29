package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
)

// request builds a request already carrying a request id, as middleware would.
func request(requestID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	if requestID != "" {
		r = r.WithContext(api.ContextWithRequestID(r.Context(), requestID))
	}
	return r
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) api.ErrorResponse {
	t.Helper()
	var body api.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestWriteJSONSetsHeadersAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteJSON(rec, request("req-1"), http.StatusCreated, map[string]string{"id": "abc"})

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.JSONEq(t, `{"id":"abc"}`, rec.Body.String())
}

func TestWriteErrorUsesTheDocumentedShape(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteError(rec, request("req-42"), http.StatusNotFound,
		api.CodeNotFound, "The requested incident does not exist.")

	require.Equal(t, http.StatusNotFound, rec.Code)

	body := decode(t, rec)
	require.Equal(t, api.CodeNotFound, body.Error)
	require.Equal(t, "The requested incident does not exist.", body.Message)
	require.Equal(t, "req-42", body.RequestID)
	require.Empty(t, body.Fields)
}

// An error raised outside a request still has to produce valid JSON.
func TestWriteErrorWithoutRequestIDOmitsIt(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteError(rec, request(""), http.StatusForbidden, api.CodeForbidden, "Nope.")

	require.NotContains(t, rec.Body.String(), "request_id")
	require.Equal(t, api.CodeForbidden, decode(t, rec).Error)
}

// Internal detail is for the operator, not the caller.
func TestWriteInternalErrorHidesTheCause(t *testing.T) {
	rec := httptest.NewRecorder()
	cause := errorWithSecrets{}
	api.WriteInternalError(rec, request("req-7"), cause)

	require.Equal(t, http.StatusInternalServerError, rec.Code)

	raw := rec.Body.String()
	require.NotContains(t, raw, "flowcast_prod")
	require.NotContains(t, raw, "SELECT")
	require.NotContains(t, raw, "hunter2")

	body := decode(t, rec)
	require.Equal(t, api.CodeInternal, body.Error)
	require.Equal(t, "req-7", body.RequestID)
}

type errorWithSecrets struct{}

func (errorWithSecrets) Error() string {
	return `pq: relation "flowcast_prod.incidents" does not exist while running SELECT ... password=hunter2`
}

func TestWriteValidationErrorListsEveryField(t *testing.T) {
	rec := httptest.NewRecorder()
	err := (&models.Incident{}).Validate()
	require.Error(t, err)

	api.WriteValidationError(rec, request("req-9"), err)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	body := decode(t, rec)
	require.Equal(t, api.CodeValidationFailed, body.Error)
	require.NotEmpty(t, body.Fields)

	fields := make([]string, len(body.Fields))
	for i, issue := range body.Fields {
		fields[i] = issue.Field
	}
	require.Contains(t, fields, "title")
	require.Contains(t, fields, "severity")
}

func TestWriteValidationErrorFallsBackForOtherErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteValidationError(rec, request("req-9"), errorWithSecrets{})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, api.CodeBadRequest, decode(t, rec).Error)
	require.NotContains(t, rec.Body.String(), "hunter2")
}

// A payload that cannot be marshalled must not leave a 200 with a truncated body.
func TestWriteJSONFailsCleanlyOnUnencodablePayload(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteJSON(rec, request("req-3"), http.StatusOK, map[string]any{
		"bad": make(chan int), // channels cannot be encoded
	})

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, api.CodeInternal, decode(t, rec).Error)
}

func TestValidRequestID(t *testing.T) {
	valid := []string{"abc123", "req-1", "a_b.c", "0123456789"}
	for _, id := range valid {
		require.True(t, api.ValidRequestID(id), "%q should be accepted", id)
	}

	invalid := map[string]string{
		"empty":         "",
		"newline":       "abc\ndef",
		"carriage":      "abc\rdef",
		"space":         "abc def",
		"quote":         `abc"def`,
		"json fragment": `","level":"ERROR","msg":"forged`,
		"too long":      string(make([]byte, api.MaxRequestIDLen+1)),
	}
	for name, id := range invalid {
		t.Run(name, func(t *testing.T) {
			require.False(t, api.ValidRequestID(id))
		})
	}
}
