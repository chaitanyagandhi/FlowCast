package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

// readyRetryInterval is how long to wait between startup connectivity attempts. The whole
// retry loop is bounded by RedisConfig.ConnectTimeout, not by an attempt count.
const readyRetryInterval = 500 * time.Millisecond

// ErrInvalidRedisURL reports an unusable connection string. The string itself is never
// included: it carries the password.
var ErrInvalidRedisURL = errors.New("parsing FLOWCAST_REDIS_URL: invalid connection string")

// Connect builds the Redis client and verifies the server is reachable before returning,
// so a misconfigured or unreachable Redis is a startup failure rather than a surprise on
// the first queued job.
//
// The caller owns the returned client and must Close it.
func Connect(ctx context.Context, cfg config.RedisConfig, logger *slog.Logger) (*redis.Client, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, ErrInvalidRedisURL
	}
	opts.DialTimeout = cfg.ConnectTimeout

	client := redis.NewClient(opts)

	if err := waitForReady(ctx, client, cfg.ConnectTimeout, logger); err != nil {
		_ = client.Close()
		return nil, err
	}

	logger.Info("connected to redis", "addr", opts.Addr, "db", opts.DB)
	return client, nil
}

// Ping reports whether Redis is answering.
func Ping(ctx context.Context, client *redis.Client) error {
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("pinging redis: %w", err)
	}
	return nil
}

// waitForReady pings until Redis answers or the timeout expires. Retrying matters under
// docker compose, where the backend can start while Redis is still booting.
func waitForReady(ctx context.Context, client *redis.Client, timeout time.Duration, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		err := client.Ping(ctx).Err()
		if err == nil {
			if attempt > 1 {
				logger.Info("redis became ready", "attempts", attempt)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("redis not reachable within %s (%d attempts): %w",
				timeout, attempt, err)
		case <-time.After(readyRetryInterval):
			logger.Warn("redis not ready, retrying", "attempt", attempt, "error", err.Error())
		}
	}
}
