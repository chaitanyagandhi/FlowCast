package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
)

// Logger emits one structured record per request.
//
// The level follows the outcome: server failures are logged at error, client mistakes at
// warn, and everything else at info. That way a log filtered to warn and above shows the
// requests that actually went wrong without any extra configuration.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := newResponseRecorder(w)

			next.ServeHTTP(recorder, r)

			elapsed := time.Since(started)
			logger.LogAttrs(r.Context(), levelForStatus(recorder.status),
				"http request",
				slog.String("request_id", api.RequestID(r.Context())),
				slog.String("method", r.Method),
				// URL.Path only: a query string can carry identifiers or tokens, and
				// this record is written for every single request.
				slog.String("path", r.URL.Path),
				slog.Int("status", recorder.status),
				slog.Int("bytes", recorder.bytes),
				slog.Int64("duration_ms", elapsed.Milliseconds()),
				slog.String("remote_ip", remoteIP(r)),
			)
		})
	}
}

func levelForStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// remoteIP returns the peer address without its port.
//
// X-Forwarded-For is deliberately ignored. Trusting it without a configured list of
// trusted proxies lets any client claim any address, which is worse than logging the
// proxy's own. Revisit when FlowCast actually runs behind one.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
