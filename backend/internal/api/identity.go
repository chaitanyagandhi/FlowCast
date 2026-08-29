package api

import (
	"context"

	"github.com/google/uuid"
)

// Identity is the authenticated caller, taken from a verified access token.
//
// TeamID is the tenant boundary. It comes from the signed token rather than from a path,
// query, or body parameter, so a caller cannot name a team they were not issued a token
// for. Every query against tenant-owned data filters on this value.
type Identity struct {
	UserID uuid.UUID
	TeamID uuid.UUID
}

const identityKey contextKey = iota + 1

// ContextWithIdentity attaches the authenticated caller to a request context.
func ContextWithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

// IdentityFrom returns the authenticated caller, and whether there is one.
//
// The boolean is not decoration: a handler mounted outside the authentication middleware
// would otherwise read a zero UUID and query team_id = '00000000-...', which matches
// nothing but says nothing either. Handlers must treat a missing identity as a bug.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey).(Identity)
	return identity, ok
}
