package api_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/api"
)

func TestIdentityRoundTripsThroughContext(t *testing.T) {
	want := api.Identity{UserID: uuid.New(), TeamID: uuid.New()}

	ctx := api.ContextWithIdentity(context.Background(), want)

	got, ok := api.IdentityFrom(ctx)
	require.True(t, ok)
	require.Equal(t, want, got)
}

// A handler outside the authentication middleware must be able to tell that there is no
// caller, rather than reading a zero team id and querying for nothing.
func TestIdentityIsReportedMissingWhenAbsent(t *testing.T) {
	got, ok := api.IdentityFrom(context.Background())

	require.False(t, ok)
	require.Equal(t, uuid.Nil, got.UserID)
	require.Equal(t, uuid.Nil, got.TeamID)
}

// The identity and request id keys must not collide.
func TestIdentityAndRequestIDCoexist(t *testing.T) {
	identity := api.Identity{UserID: uuid.New(), TeamID: uuid.New()}

	ctx := api.ContextWithIdentity(
		api.ContextWithRequestID(context.Background(), "req-1"), identity)

	got, ok := api.IdentityFrom(ctx)
	require.True(t, ok)
	require.Equal(t, identity, got)
	require.Equal(t, "req-1", api.RequestID(ctx))
}

// A later identity replaces an earlier one rather than merging with it.
func TestIdentityIsOverriddenByALaterValue(t *testing.T) {
	first := api.Identity{UserID: uuid.New(), TeamID: uuid.New()}
	second := api.Identity{UserID: uuid.New(), TeamID: uuid.New()}

	ctx := api.ContextWithIdentity(api.ContextWithIdentity(context.Background(), first), second)

	got, _ := api.IdentityFrom(ctx)
	require.Equal(t, second, got)
}
