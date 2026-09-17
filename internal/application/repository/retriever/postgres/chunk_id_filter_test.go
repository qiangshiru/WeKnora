package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChunkIDFilterClause(t *testing.T) {
	clause, values := chunkIDFilterClause([]string{"chunk-1", "chunk-2"}, "chunk_id")

	require.Equal(t, "chunk_id IN (?, ?)", clause)
	require.Equal(t, []interface{}{"chunk-1", "chunk-2"}, values)
}

func TestChunkIDFilterClauseSkipsEmptyList(t *testing.T) {
	clause, values := chunkIDFilterClause(nil, "chunk_id")

	require.Empty(t, clause)
	require.Nil(t, values)
}

func TestChunkIDFilterClauseKeepsValuesOutOfSQL(t *testing.T) {
	hostile := `x'); DROP TABLE embeddings; --`
	clause, values := chunkIDFilterClause([]string{hostile}, "chunk_id")

	require.Equal(t, "chunk_id IN (?)", clause)
	require.NotContains(t, clause, hostile)
	require.Equal(t, []interface{}{hostile}, values)
}
