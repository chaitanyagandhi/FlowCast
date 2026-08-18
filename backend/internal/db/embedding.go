package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrEmbeddingDimensionMismatch reports that the configured embedding size does not match
// the width of the embedding column.
var ErrEmbeddingDimensionMismatch = errors.New("embedding dimension mismatch")

// VerifyEmbeddingDimensions checks that incidents.embedding is as wide as the configured
// embedding model produces.
//
// A pgvector column has a fixed width baked into the schema, while the model that fills it
// is chosen at runtime. Pointing FLOWCAST_EMBEDDING_MODEL at a model of a different size
// would otherwise surface much later as a confusing insert failure, so this is checked
// once at startup instead.
func VerifyEmbeddingDimensions(ctx context.Context, pool *pgxpool.Pool, want int) error {
	const query = `
		SELECT atttypmod
		FROM pg_attribute
		WHERE attrelid = 'incidents'::regclass
		  AND attname = 'embedding'
		  AND NOT attisdropped`

	var got int
	if err := pool.QueryRow(ctx, query).Scan(&got); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("incidents.embedding column not found; are migrations applied?")
		}
		return fmt.Errorf("reading embedding column width: %w", err)
	}

	if got != want {
		return fmt.Errorf(
			"%w: incidents.embedding is vector(%d) but FLOWCAST_EMBEDDING_DIMENSIONS is %d",
			ErrEmbeddingDimensionMismatch, got, want)
	}
	return nil
}
