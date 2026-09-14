package postgres

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChunkMetadataFilterClauseBuildsParameterizedSemijoin(t *testing.T) {
	filter := types.JSONMap{
		"分类":      "规则",
		"is_body": true,
	}

	clause, value, err := chunkMetadataFilterClause(filter, "?")

	require.NoError(t, err)
	require.Contains(t, clause, "filter_chunks.id = embeddings.chunk_id")
	require.Contains(t, clause, "filter_chunks.metadata @> ?::jsonb")
	require.NotContains(t, clause, "规则")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(value), &decoded))
	require.Equal(t, "规则", decoded["分类"])
	require.Equal(t, true, decoded["is_body"])
}

func TestChunkMetadataFilterClauseSkipsEmptyFilter(t *testing.T) {
	clause, value, err := chunkMetadataFilterClause(nil, "?")

	require.NoError(t, err)
	require.Empty(t, clause)
	require.Empty(t, value)
}

func TestChunkMetadataFilterClauseRejectsUnserializableValue(t *testing.T) {
	_, _, err := chunkMetadataFilterClause(types.JSONMap{"invalid": func() {}}, "?")

	require.ErrorContains(t, err, "marshal chunk metadata filter")
}
