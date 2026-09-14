package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestShouldSkipContextEnrichmentForMetadataFilter(t *testing.T) {
	require.False(t, shouldSkipContextEnrichment(types.SearchParams{}))
	require.True(t, shouldSkipContextEnrichment(types.SearchParams{SkipContextEnrichment: true}))
	require.True(t, shouldSkipContextEnrichment(types.SearchParams{
		ChunkMetadataFilter: types.JSONMap{"分类": "规则", "is_body": true},
	}))
}
