// Package queue implements the Redis-backed job queue used to move expensive work out
// of the request path, plus webhook idempotency keys and short-lived processing state.
// PostgreSQL, not Redis, remains the durable source of truth.
package queue
