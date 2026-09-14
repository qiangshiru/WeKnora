package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type preChunkKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	knowledge *types.Knowledge
}

func (r *preChunkKnowledgeRepo) GetKnowledgeByID(context.Context, uint64, string) (*types.Knowledge, error) {
	return r.knowledge, nil
}

func (r *preChunkKnowledgeRepo) UpdateKnowledge(_ context.Context, knowledge *types.Knowledge) error {
	r.knowledge = knowledge
	return nil
}

type preChunkChunkRepo struct {
	interfaces.ChunkRepository
	created []*types.Chunk
}

func (r *preChunkChunkRepo) DeleteChunksByKnowledgeID(context.Context, uint64, string) error {
	return nil
}

func (r *preChunkChunkRepo) CreateChunks(_ context.Context, chunks []*types.Chunk) error {
	r.created = append([]*types.Chunk(nil), chunks...)
	return nil
}

type preChunkEmbedder struct{}

func (preChunkEmbedder) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }
func (preChunkEmbedder) BatchEmbed(context.Context, []string) ([][]float32, error) {
	return [][]float32{{1}}, nil
}
func (preChunkEmbedder) BatchEmbedWithPool(context.Context, embedding.Embedder, []string) ([][]float32, error) {
	return [][]float32{{1}}, nil
}
func (preChunkEmbedder) GetModelName() string { return "pre-chunk-test" }
func (preChunkEmbedder) GetDimensions() int   { return 1 }
func (preChunkEmbedder) GetModelID() string   { return "pre-chunk-test" }

type preChunkModelService struct{ interfaces.ModelService }

func (preChunkModelService) GetEmbeddingModel(context.Context, string) (embedding.Embedder, error) {
	return preChunkEmbedder{}, nil
}

type preChunkRetrieveEngine struct {
	interfaces.RetrieveEngineService
	indexed []*types.IndexInfo
}

func (e *preChunkRetrieveEngine) EngineType() types.RetrieverEngineType {
	return types.PostgresRetrieverEngineType
}
func (e *preChunkRetrieveEngine) Support() []types.RetrieverType {
	return []types.RetrieverType{types.VectorRetrieverType}
}
func (e *preChunkRetrieveEngine) DeleteByKnowledgeIDList(context.Context, []string, int, string) error {
	return nil
}
func (e *preChunkRetrieveEngine) EstimateStorageSize(context.Context, embedding.Embedder, []*types.IndexInfo, []types.RetrieverType) int64 {
	return 0
}
func (e *preChunkRetrieveEngine) BatchIndex(_ context.Context, _ embedding.Embedder, infos []*types.IndexInfo, _ []types.RetrieverType) error {
	e.indexed = append([]*types.IndexInfo(nil), infos...)
	return nil
}

type preChunkRegistry struct {
	interfaces.RetrieveEngineRegistry
	engine interfaces.RetrieveEngineService
}

func (r preChunkRegistry) GetRetrieveEngineService(types.RetrieverEngineType) (interfaces.RetrieveEngineService, error) {
	return r.engine, nil
}

type preChunkGraphRepo struct {
	interfaces.RetrieveGraphRepository
}

func (preChunkGraphRepo) DelGraph(context.Context, []types.NameSpace) error { return nil }

type preChunkTenantRepo struct{ interfaces.TenantRepository }

func (preChunkTenantRepo) AdjustStorageUsed(context.Context, uint64, int64) error { return nil }

type preChunkTaskEnqueuer struct{ calls int }

func (s *preChunkTaskEnqueuer) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	s.calls++
	return &asynq.TaskInfo{}, nil
}

func TestProcessPreChunkedKnowledgeIndexesExactlyOneChunkWithoutPostProcess(t *testing.T) {
	knowledge := &types.Knowledge{
		ID:              "knowledge-1",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		ParseStatus:     types.ParseStatusPending,
	}
	chunkRepo := &preChunkChunkRepo{}
	retrieveEngine := &preChunkRetrieveEngine{}
	tasks := &preChunkTaskEnqueuer{}
	tenant := &types.Tenant{ID: 1, RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{{
		RetrieverType:       types.VectorRetrieverType,
		RetrieverEngineType: types.PostgresRetrieverEngineType,
	}}}}
	ctx := context.WithValue(context.Background(), types.TenantInfoContextKey, tenant)
	svc := &knowledgeService{
		repo:           &preChunkKnowledgeRepo{knowledge: knowledge},
		chunkRepo:      chunkRepo,
		modelService:   preChunkModelService{},
		retrieveEngine: preChunkRegistry{engine: retrieveEngine},
		graphEngine:    preChunkGraphRepo{},
		tenantRepo:     preChunkTenantRepo{},
		task:           tasks,
	}
	kb := &types.KnowledgeBase{
		ID:               "kb-1",
		TenantID:         1,
		EmbeddingModelID: "embedding-1",
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}

	svc.processPreChunkedKnowledge(ctx, kb, knowledge, []types.ParsedChunk{
		{Content: "semantic prefix\n\noriginal content", Seq: 1, Start: 0, End: 33},
		{Content: "second prefix\n\nsecond content", Seq: 2, Start: 34, End: 63},
	})

	require.Len(t, chunkRepo.created, 2)
	require.Equal(t, "semantic prefix\n\noriginal content", chunkRepo.created[0].Content)
	require.Len(t, retrieveEngine.indexed, 2)
	require.Equal(t, chunkRepo.created[0].ID, retrieveEngine.indexed[0].ChunkID)
	require.Equal(t, types.ParseStatusCompleted, knowledge.ParseStatus)
	require.Equal(t, "enabled", knowledge.EnableStatus)
	require.Zero(t, tasks.calls)
}

func TestValidatePreChunkedMetadata(t *testing.T) {
	metadata, err := validatePreChunkedDocumentMetadata(map[string]any{
		"scope": "交易业务",
		"empty": "  ",
	})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"scope": "交易业务"}, metadata)
	_, err = validatePreChunkedDocumentMetadata(map[string]any{"nested": []string{"nested"}})
	require.Error(t, err)
}
