package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// MaxRequestBodyBytes caps a decoded request body. Without a limit, a single request can
// make the process allocate until it dies. FlowCast's payloads are small; webhook
// ingestion sets its own, larger, bound where it needs one.
const MaxRequestBodyBytes = 1 << 20 // 1 MiB

// DecodeJSON reads a JSON request body into dst.
//
// Every error it returns is already safe to show a caller: the messages are written here
// rather than passed through from encoding/json, which reports offsets and Go type names.
//
// Unknown fields are rejected. A client sending "emial" should be told, not silently
// registered with an empty email; the same applies to a field that was renamed and left
// behind in a stale client.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := requireJSONContentType(r); err != nil {
		return err
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return decodeError(err)
	}

	// A body with trailing content is malformed, not merely odd: it usually means two
	// documents were concatenated and only the first would have taken effect.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}

	return nil
}

func requireJSONContentType(r *http.Request) error {
	raw := r.Header.Get("Content-Type")
	if raw == "" {
		return errors.New("Content-Type must be application/json")
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	return nil
}

// decodeError translates a decoder failure into something a caller can act on.
func decodeError(err error) error {
	var (
		syntaxErr       *json.SyntaxError
		unmarshalTypeEr *json.UnmarshalTypeError
		maxBytesErr     *http.MaxBytesError
	)

	switch {
	case errors.Is(err, io.EOF):
		return errors.New("request body must not be empty")

	case errors.As(err, &maxBytesErr):
		return fmt.Errorf("request body must not exceed %d bytes", MaxRequestBodyBytes)

	case errors.As(err, &syntaxErr):
		return errors.New("request body is not valid JSON")

	case errors.As(err, &unmarshalTypeEr):
		// Naming the field is useful and safe; the Go type behind it is neither.
		return fmt.Errorf("field %q has the wrong type", unmarshalTypeEr.Field)

	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return fmt.Errorf("unknown field %s", field)

	default:
		return errors.New("request body is not valid JSON")
	}
}
