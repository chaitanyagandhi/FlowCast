package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/queue"
)

func newSessionStore(t *testing.T) *queue.SessionStore {
	t.Helper()
	cfg := integrationConfig(t) // skips when FLOWCAST_TEST_REDIS_URL is unset

	client, err := queue.Connect(context.Background(), cfg, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return queue.NewSessionStore(client)
}

func TestRevokedTokenIsReportedRevoked(t *testing.T) {
	store := newSessionStore(t)
	ctx := context.Background()
	tokenID := uuid.New()

	revoked, err := store.IsRevoked(ctx, tokenID)
	require.NoError(t, err)
	require.False(t, revoked, "an unknown token has not been revoked")

	require.NoError(t, store.Revoke(ctx, tokenID, time.Minute))

	revoked, err = store.IsRevoked(ctx, tokenID)
	require.NoError(t, err)
	require.True(t, revoked)
}

// Revocations are per token: logging one session out must not end the others.
func TestRevocationIsScopedToOneToken(t *testing.T) {
	store := newSessionStore(t)
	ctx := context.Background()
	revokedID, otherID := uuid.New(), uuid.New()

	require.NoError(t, store.Revoke(ctx, revokedID, time.Minute))

	other, err := store.IsRevoked(ctx, otherID)
	require.NoError(t, err)
	require.False(t, other, "an unrelated session must stay valid")
}

// The entry only has to outlive the token it cancels, so it self-expires and the store
// never grows without bound.
func TestRevocationExpires(t *testing.T) {
	store := newSessionStore(t)
	ctx := context.Background()
	tokenID := uuid.New()

	require.NoError(t, store.Revoke(ctx, tokenID, 300*time.Millisecond))

	revoked, err := store.IsRevoked(ctx, tokenID)
	require.NoError(t, err)
	require.True(t, revoked)

	require.Eventually(t, func() bool {
		revoked, err := store.IsRevoked(ctx, tokenID)
		return err == nil && !revoked
	}, 3*time.Second, 100*time.Millisecond, "the entry should expire on its own")
}

// A token that has already expired needs no record: signature validation refuses it
// anyway, and storing it would be a leak with no benefit.
func TestRevokingAnExpiredTokenStoresNothing(t *testing.T) {
	store := newSessionStore(t)
	ctx := context.Background()
	tokenID := uuid.New()

	require.NoError(t, store.Revoke(ctx, tokenID, 0))
	require.NoError(t, store.Revoke(ctx, tokenID, -time.Hour))

	revoked, err := store.IsRevoked(ctx, tokenID)
	require.NoError(t, err)
	require.False(t, revoked)
}

func TestRevokeIsIdempotent(t *testing.T) {
	store := newSessionStore(t)
	ctx := context.Background()
	tokenID := uuid.New()

	for range 3 {
		require.NoError(t, store.Revoke(ctx, tokenID, time.Minute))
	}

	revoked, err := store.IsRevoked(ctx, tokenID)
	require.NoError(t, err)
	require.True(t, revoked)
}

// An unreachable Redis must surface as an error, never as "not revoked".
func TestIsRevokedReportsAClosedClient(t *testing.T) {
	cfg := integrationConfig(t)

	client, err := queue.Connect(context.Background(), cfg, discardLogger())
	require.NoError(t, err)
	require.NoError(t, client.Close())

	store := queue.NewSessionStore(client)

	revoked, err := store.IsRevoked(context.Background(), uuid.New())
	require.Error(t, err, "a failure must not be reported as 'not revoked'")
	require.False(t, revoked)

	require.Error(t, store.Revoke(context.Background(), uuid.New(), time.Minute))
}
