//go:build depsref

// Package depsref pins FlowCast's approved third-party dependency set so that
// `go mod tidy` keeps it in go.mod before the packages that use each library exist.
//
// The `depsref` build tag is never enabled, so nothing here is compiled into the binary.
// `go mod tidy` still reads the file — it considers all build configurations — which is
// exactly the behaviour this relies on. Each import is deleted from this file as the real
// package that needs it lands, and the file itself is removed once the list is empty.
package depsref

import (
	// HTTP routing and CORS.
	_ "github.com/go-chi/chi/v5"
	_ "github.com/go-chi/cors"

	// pgvector support for embedding columns.
	_ "github.com/pgvector/pgvector-go"

	// Redis client for queues and idempotency.
	_ "github.com/redis/go-redis/v9"

	// Authentication: JWT signing and bcrypt password hashing.
	_ "github.com/golang-jwt/jwt/v5"
	_ "golang.org/x/crypto/bcrypt"

	// Public identifiers.
	_ "github.com/google/uuid"

	// AI provider SDK.
	_ "github.com/openai/openai-go/v3"
)
