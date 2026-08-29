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
	// pgvector support for embedding columns.
	_ "github.com/pgvector/pgvector-go"

	// Redis client for queues and idempotency.
	_ "github.com/redis/go-redis/v9"

	// AI provider SDK.
	_ "github.com/openai/openai-go/v3"
)
