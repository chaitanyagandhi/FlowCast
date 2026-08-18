package server_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/server"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func testConfig(shutdownTimeout time.Duration) config.ServerConfig {
	return config.ServerConfig{
		Port:            0,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: shutdownTimeout,
	}
}

// listen binds an ephemeral local port so tests never collide with a real service.
func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return ln
}

// start runs the server in the background and returns its base URL plus a channel
// carrying the eventual result of Serve.
func start(t *testing.T, ctx context.Context, cfg config.ServerConfig, handler http.Handler) (string, <-chan error) {
	t.Helper()

	ln := listen(t)
	srv := server.New(cfg, handler, discardLogger())

	result := make(chan error, 1)
	go func() { result <- srv.Serve(ctx, ln) }()

	return "http://" + ln.Addr().String(), result
}

func TestServesRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "flowcast")
	})

	baseURL, result := start(t, ctx, testConfig(5*time.Second), handler)

	resp, err := http.Get(baseURL + "/anything")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusTeapot, resp.StatusCode)
	require.Equal(t, "flowcast", string(body))

	cancel()
	require.NoError(t, <-result)
}

// Cancelling the context must stop the server, and Serve must report a clean stop.
func TestShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	baseURL, result := start(t, ctx, testConfig(5*time.Second),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	resp, err := http.Get(baseURL + "/")
	require.NoError(t, err)
	resp.Body.Close()

	cancel()

	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after context cancellation")
	}

	// The port is no longer being served.
	_, err = http.Get(baseURL + "/")
	require.Error(t, err)
}

// This is the point of graceful shutdown: a request already being handled when the signal
// arrives still gets its response.
func TestInFlightRequestCompletesDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handlerStarted := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handlerStarted)
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "finished")
	})

	baseURL, result := start(t, ctx, testConfig(5*time.Second), handler)

	type response struct {
		status int
		body   string
		err    error
	}
	responses := make(chan response, 1)

	go func() {
		resp, err := http.Get(baseURL + "/slow")
		if err != nil {
			responses <- response{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		responses <- response{status: resp.StatusCode, body: string(body), err: err}
	}()

	// Cancel only once the handler is definitely running.
	<-handlerStarted
	cancel()

	got := <-responses
	require.NoError(t, got.err, "an in-flight request must not be dropped")
	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, "finished", got.body)

	require.NoError(t, <-result)
}

// A handler that outlasts the grace period does not get to block the process forever.
func TestShutdownForcesCloseAfterGracePeriod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handlerStarted)
		<-releaseHandler
		w.WriteHeader(http.StatusOK)
	})

	baseURL, result := start(t, ctx, testConfig(200*time.Millisecond), handler)

	go func() {
		resp, err := http.Get(baseURL + "/stuck")
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-handlerStarted
	start := time.Now()
	cancel()

	select {
	case err := <-result:
		require.Error(t, err, "an expired grace period is reported, not swallowed")
		require.Contains(t, err.Error(), "graceful shutdown exceeded")
		require.Less(t, time.Since(start), 3*time.Second,
			"the server must not wait for the stuck handler")
	case <-time.After(5 * time.Second):
		t.Fatal("server hung past its grace period")
	}

	close(releaseHandler)
}

// Run binds the configured port itself, so a port already in use has to be reported
// rather than discovered later.
func TestRunReportsUnavailablePort(t *testing.T) {
	// Occupy the port on every interface, matching the wildcard address Run binds to.
	occupied, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer occupied.Close()

	_, portStr, err := net.SplitHostPort(occupied.Addr().String())
	require.NoError(t, err)

	cfg := testConfig(time.Second)
	cfg.Port = mustAtoi(t, portStr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := server.New(cfg, http.NotFoundHandler(), discardLogger())

	// Run in the background: if the bind unexpectedly succeeds, Run serves until the
	// context is cancelled, and this must fail the test rather than hang the suite.
	result := make(chan error, 1)
	go func() { result <- srv.Run(ctx) }()

	select {
	case err := <-result:
		require.Error(t, err)
		require.Contains(t, err.Error(), "listening on")
	case <-time.After(3 * time.Second):
		cancel()
		<-result
		t.Fatal("Run did not report the port conflict")
	}
}

// A server whose listener dies underneath it reports the failure rather than returning as
// though it had been asked to stop.
func TestServeReportsListenerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln := listen(t)
	srv := server.New(testConfig(time.Second), http.NotFoundHandler(), discardLogger())

	result := make(chan error, 1)
	go func() { result <- srv.Serve(ctx, ln) }()

	// Give Serve a moment to start, then pull the listener out from under it.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, ln.Close())

	select {
	case err := <-result:
		require.Error(t, err)
		require.Contains(t, err.Error(), "http server")
	case <-time.After(5 * time.Second):
		t.Fatal("closing the listener did not surface an error")
	}
}

func TestConfiguredTimeoutsAreApplied(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testConfig(time.Second)
	cfg.ReadTimeout = 120 * time.Millisecond

	baseURL, result := start(t, ctx, cfg, http.NotFoundHandler())
	defer func() { cancel(); <-result }()

	host := baseURL[len("http://"):]
	conn, err := net.Dial("tcp", host)
	require.NoError(t, err)
	defer conn.Close()

	// Send a partial request and never finish it. ReadHeaderTimeout should end it.
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n"))
	require.NoError(t, err)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	buf := make([]byte, 64)
	_, err = conn.Read(buf)

	// Either the server closed the connection or answered 408; both mean the timeout
	// fired rather than the connection being held open indefinitely.
	if err != nil {
		require.True(t, errors.Is(err, io.EOF) || isConnectionClosed(err),
			"expected the server to drop the stalled connection, got %v", err)
	}
}

func isConnectionClosed(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return err != nil
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		require.True(t, r >= '0' && r <= '9', "port %q is not numeric", s)
		n = n*10 + int(r-'0')
	}
	return n
}
