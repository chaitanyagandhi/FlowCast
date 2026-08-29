package queue_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
	"github.com/chaitanyagandhi/flowcast/backend/internal/queue"
)

// testRedisURLEnv points the integration tests at a live Redis. When it is unset those
// tests skip, so `go test ./...` still passes on a machine without one.
const testRedisURLEnv = "FLOWCAST_TEST_REDIS_URL"

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func integrationConfig(t *testing.T) config.RedisConfig {
	t.Helper()
	url := os.Getenv(testRedisURLEnv)
	if url == "" {
		t.Skipf("set %s to run redis integration tests", testRedisURLEnv)
	}
	return config.RedisConfig{URL: url, ConnectTimeout: 5 * time.Second}
}

func TestConnectRejectsInvalidURL(t *testing.T) {
	cfg := config.RedisConfig{URL: "://not a redis url", ConnectTimeout: time.Second}

	client, err := queue.Connect(context.Background(), cfg, discardLogger())
	require.ErrorIs(t, err, queue.ErrInvalidRedisURL)
	require.Nil(t, client)
}

// A parse failure must not echo the connection string back: it carries the password.
func TestConnectErrorDoesNotLeakPassword(t *testing.T) {
	cfg := config.RedisConfig{
		URL:            "redis://user:hunter2-secret@:::bad-port/0",
		ConnectTimeout: time.Second,
	}

	_, err := queue.Connect(context.Background(), cfg, discardLogger())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2-secret")
}

// An unreachable Redis is a startup failure, not something discovered on the first job.
func TestConnectFailsWhenRedisUnreachable(t *testing.T) {
	cfg := config.RedisConfig{
		URL:            "redis://127.0.0.1:1/0",
		ConnectTimeout: 900 * time.Millisecond,
	}

	started := time.Now()
	client, err := queue.Connect(context.Background(), cfg, discardLogger())

	require.Error(t, err)
	require.Nil(t, client, "the client must be closed and not handed back on failure")
	require.Contains(t, err.Error(), "not reachable")
	require.GreaterOrEqual(t, time.Since(started), 500*time.Millisecond,
		"it should retry rather than give up on the first refusal")
}

func TestConnectHonoursCallerContext(t *testing.T) {
	cfg := config.RedisConfig{URL: "redis://127.0.0.1:1/0", ConnectTimeout: time.Minute}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := queue.Connect(ctx, cfg, discardLogger())

	require.Error(t, err)
	require.Less(t, time.Since(started), 10*time.Second,
		"a cancelled caller should not wait out ConnectTimeout")
}

// --- Integration: requires a live Redis ---

func TestConnectAgainstLiveRedis(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	client, err := queue.Connect(ctx, cfg, discardLogger())
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, queue.Ping(ctx, client))
}

func TestPingReportsAClosedClient(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	client, err := queue.Connect(ctx, cfg, discardLogger())
	require.NoError(t, err)
	require.NoError(t, client.Close())

	// This is exactly what the health endpoint must notice.
	err = queue.Ping(ctx, client)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pinging redis")
}

func TestLiveRedisRoundTripsAValue(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	client, err := queue.Connect(ctx, cfg, discardLogger())
	require.NoError(t, err)
	defer client.Close()

	key := "flowcast:test:" + t.Name()
	t.Cleanup(func() { client.Del(context.Background(), key) })

	require.NoError(t, client.Set(ctx, key, "queued", time.Minute).Err())

	got, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, "queued", got)
}
