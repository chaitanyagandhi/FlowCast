package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// revokedKeyPrefix namespaces revoked refresh tokens.
const revokedKeyPrefix = "flowcast:auth:revoked:"

// SessionStore records which refresh tokens have been withdrawn.
//
// Redis rather than PostgreSQL because every entry is short-lived and self-expiring: a
// revocation only has to outlive the token it cancels, and Redis deletes it on schedule
// without a cleanup job. Nothing durable is lost if Redis is wiped -- the tokens it was
// tracking expire on their own.
type SessionStore struct {
	client *redis.Client
}

// NewSessionStore builds a revocation store over a Redis client.
func NewSessionStore(client *redis.Client) *SessionStore {
	return &SessionStore{client: client}
}

// Revoke withdraws a refresh token for the remainder of its lifetime.
//
// The entry expires with the token, so the store never grows without bound: an expired
// token is already refused by signature validation and needs no record.
func (s *SessionStore) Revoke(ctx context.Context, tokenID uuid.UUID, ttl time.Duration) error {
	if ttl <= 0 {
		// Already expired. Storing it would be a leak with no benefit.
		return nil
	}
	if err := s.client.Set(ctx, revokedKey(tokenID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("revoking token: %w", err)
	}
	return nil
}

// IsRevoked reports whether a refresh token has been withdrawn.
//
// An error is returned rather than a default, deliberately. Guessing "not revoked" when
// Redis is unreachable would quietly re-enable every token anyone had logged out of.
func (s *SessionStore) IsRevoked(ctx context.Context, tokenID uuid.UUID) (bool, error) {
	err := s.client.Get(ctx, revokedKey(tokenID)).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, redis.Nil):
		return false, nil
	default:
		return false, fmt.Errorf("checking token revocation: %w", err)
	}
}

func revokedKey(tokenID uuid.UUID) string {
	return revokedKeyPrefix + tokenID.String()
}
