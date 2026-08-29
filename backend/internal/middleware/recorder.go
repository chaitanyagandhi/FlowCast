package middleware

import "net/http"

// responseRecorder observes the status and size of a response on its way out, which the
// logging middleware needs and net/http does not expose.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	// A handler that writes a body without calling WriteHeader has implicitly sent 200.
	return &responseRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Written reports whether the status line has already gone out, which tells the panic
// handler whether a clean error response is still possible.
func (r *responseRecorder) Written() bool { return r.wroteHeader }

// Unwrap exposes the underlying writer so http.ResponseController can still reach
// optional behaviour such as Flush and Hijack through this wrapper.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
