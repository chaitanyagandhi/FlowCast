package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
)

type payload struct {
	Email string `json:"email"`
	Count int    `json:"count"`
}

// decode runs DecodeJSON against a request built from a body and content type.
func decodeBody(t *testing.T, body string, contentType string) (payload, error) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/anything", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	var dst payload
	return dst, api.DecodeJSON(httptest.NewRecorder(), req, &dst)
}

func TestDecodeJSONReadsAValidBody(t *testing.T) {
	got, err := decodeBody(t, `{"email":"ada@example.com","count":3}`, "application/json")
	require.NoError(t, err)
	require.Equal(t, "ada@example.com", got.Email)
	require.Equal(t, 3, got.Count)
}

func TestDecodeJSONAcceptsCharsetParameter(t *testing.T) {
	_, err := decodeBody(t, `{"email":"a@b.com"}`, "application/json; charset=utf-8")
	require.NoError(t, err)
}

func TestDecodeJSONRequiresJSONContentType(t *testing.T) {
	for name, contentType := range map[string]string{
		"missing":   "",
		"form":      "application/x-www-form-urlencoded",
		"text":      "text/plain",
		"malformed": "application/",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeBody(t, `{"email":"a@b.com"}`, contentType)
			require.Error(t, err)
			require.Contains(t, err.Error(), "application/json")
		})
	}
}

// A stale client sending a renamed field should be told, not silently accepted with the
// value dropped.
func TestDecodeJSONRejectsAndNamesUnknownFields(t *testing.T) {
	_, err := decodeBody(t, `{"email":"a@b.com","is_admin":true}`, "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown field")
	require.Contains(t, err.Error(), "is_admin")
}

func TestDecodeJSONReportsAnEmptyBody(t *testing.T) {
	_, err := decodeBody(t, ``, "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")
}

func TestDecodeJSONReportsMalformedJSON(t *testing.T) {
	for _, body := range []string{`{`, `{"email":}`, `not json at all`, `[1,2,3`} {
		_, err := decodeBody(t, body, "application/json")
		require.Error(t, err, "body %q should be rejected", body)
	}
}

// The field is named because that is actionable; the Go type behind it is not mentioned.
func TestDecodeJSONReportsAWrongFieldTypeWithoutLeakingGoTypes(t *testing.T) {
	_, err := decodeBody(t, `{"count":"three"}`, "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "count")
	require.NotContains(t, err.Error(), "int")
	require.NotContains(t, err.Error(), "struct")
}

// Two concatenated documents usually mean only the first would have taken effect, which
// is worth refusing rather than silently honouring.
func TestDecodeJSONRejectsTrailingContent(t *testing.T) {
	_, err := decodeBody(t, `{"email":"a@b.com"}{"email":"c@d.com"}`, "application/json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "single JSON object")
}

// Without a cap, one request can make the process allocate until it dies. This is the path
// the handler tests never reach.
func TestDecodeJSONRefusesAnOversizedBody(t *testing.T) {
	oversized := `{"email":"` + strings.Repeat("a", api.MaxRequestBodyBytes+1) + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/anything", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")

	var dst payload
	err := api.DecodeJSON(httptest.NewRecorder(), req, &dst)

	require.Error(t, err)
	require.Contains(t, err.Error(), "must not exceed")
}

// A body just under the limit is still accepted, so the bound is a cap rather than an
// accidental smaller one.
func TestDecodeJSONAcceptsABodyUnderTheLimit(t *testing.T) {
	// Leave room for the JSON scaffolding around the value.
	value := strings.Repeat("a", api.MaxRequestBodyBytes-64)
	body := `{"email":"` + value + `"}`
	require.Less(t, len(body), api.MaxRequestBodyBytes)

	got, err := decodeBody(t, body, "application/json")
	require.NoError(t, err)
	require.Len(t, got.Email, len(value))
}

// Decoder errors are shown to callers, so none of them may carry byte offsets or Go
// internals that encoding/json would otherwise supply.
func TestDecodeJSONErrorsAreCallerSafe(t *testing.T) {
	bodies := []string{`{`, ``, `{"count":"x"}`, `{"nope":1}`, `{"a":1}{"b":2}`}

	for _, body := range bodies {
		_, err := decodeBody(t, body, "application/json")
		require.Error(t, err)

		message := err.Error()
		require.NotContains(t, message, "offset", "message leaks a byte offset: %s", message)
		require.NotContains(t, message, "api.payload", "message leaks a Go type: %s", message)
		require.NotContains(t, message, "json: cannot unmarshal",
			"raw encoding/json text reached the caller: %s", message)
	}
}
