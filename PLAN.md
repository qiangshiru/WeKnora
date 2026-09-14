# `POST /knowledge-bases/{id}/hybrid-search`：Chunk Metadata 条件检索

## 结论

可以修改，且不需要改变现有数据库字段。

PostgreSQL 中 `public.chunks.metadata` 已是 JSONB；`public.chunks.id` 可以与
`public.embeddings.chunk_id` 关联。因此可在向量召回与关键词召回的候选 SQL
内部增加参数化的 JSONB 包含条件，实现 chunk 级强过滤。

本方案不新增 `embeddings.category`，不新增迁移，不复制 metadata 到 embeddings。

## 范围

- 只修改 `POST /api/v1/knowledge-bases/{id}/hybrid-search` 使用的检索参数链路。
- 只在 PostgreSQL retriever 中实现过滤。
- 向量与关键词两路召回都必须在 TopK、阈值过滤和融合前应用条件。
- 传入过滤条件时关闭父/相邻/关系 chunk 扩展，保证最终返回的每个 chunk 都满足条件。
- 不修改搜索结果结构。
- 不修改 `chunks` 或 `embeddings` 的现有字段。
- 不修改其它检索后端。

## 接口契约

请求新增可选字段 `chunk_metadata_filter`，对象内所有字段使用 AND 语义；字段值按
JSON 类型精确匹配。空对象或不传表示不启用 metadata 过滤。

示例：只检索 metadata 同时包含 `分类 = 规则` 且 `is_body = true` 的 chunk：

```json
{
  "query_text": "请说明适用规则",
  "match_count": 10,
  "vector_threshold": 0.5,
  "keyword_threshold": 0.3,
  "chunk_metadata_filter": {
    "分类": "规则",
    "is_body": true
  }
}
```

对应的核心 SQL 条件：

```sql
EXISTS (
    SELECT 1
    FROM public.chunks AS filter_chunks
    WHERE filter_chunks.id = embeddings.chunk_id
      AND filter_chunks.metadata @> ?::jsonb
)
```

该 `?` 由 GORM 绑定为：

```json
{"分类":"规则","is_body":true}
```

使用参数化 JSON，而不是把 key/value 拼接到 SQL，避免 SQL 注入和中文字段名转义问题。
向量检索 SQL 全部使用 GORM 可识别的 `?` 占位符；查询向量在距离计算和
HNSW 排序表达式中各出现一次，因此参数列表中也显式绑定两次，避免 PostgreSQL
报 `expected N arguments, got 0`。

## 设计决定

### 使用 `EXISTS` 半连接

- 明确使用 `chunks.id = embeddings.chunk_id` 关联。
- 一个 chunk 可能对应正文、生成问题等多条 embedding；`EXISTS` 不会额外复制行。
- metadata 仍以 `chunks` 为唯一事实来源，更新 chunk metadata 后无需重建向量数据。

### 使用 JSONB `@>`

- 支持一次传入多个字段，并按 AND 语义精确包含匹配。
- 支持字符串、布尔、数字、对象和数组等合法 JSON 值。
- 示例中的 `is_body` 必须是 JSON 布尔值 `true`，不能传字符串 `"true"`。

### 过滤位置

- 向量召回：条件位于 HNSW 候选子查询内部、`ORDER BY distance LIMIT` 之前。
- 关键词召回：条件与 ParadeDB 的 `content ||| query` 位于同一个 WHERE 中、LIMIT 之前。
- 这样过滤属于召回前强过滤，而不是融合后的结果过滤。
- 过滤请求自动跳过上下文扩展；否则扩展阶段可能追加未满足 metadata 条件的关联 chunk。

## 改动文件

| 文件 | 改动 |
| --- | --- |
| `internal/types/search.go` | `SearchParams` 新增 `chunk_metadata_filter` |
| `internal/types/retriever.go` | `RetrieveParams` 新增 `ChunkMetadataFilter` |
| `client/knowledgebase.go` | Go SDK 的 `SearchParams` 暴露同名过滤字段 |
| `internal/application/service/knowledgebase_search.go` | 向量、关键词参数透传；过滤请求关闭上下文扩展；trace 只记录过滤字段数量，不记录值 |
| `internal/application/service/knowledgebase_search_metadata_filter_test.go` | 验证过滤请求强制关闭上下文扩展 |
| `internal/application/repository/retriever/postgres/repository.go` | 生成参数化 `EXISTS + JSONB @>` 条件并用于两路召回 |
| `internal/application/repository/retriever/postgres/metadata_filter_test.go` | 验证关联条件、占位符参数化和空过滤行为 |
| `docs/api/knowledge-base.md` | 增加参数、请求示例与 JSON 类型说明 |

明确不再需要：

- `migrations/versioned/000093_embeddings_category.*`
- `embeddings.category` 字段或索引
- 索引写入、更新、复制链路中的 category 同步逻辑

## 验收

### 自动化验证

```bash
go test ./internal/application/repository/retriever/postgres
go test ./internal/application/service -run 'MetadataFilter|KnowledgebaseSearch|HybridSearch'
go test ./internal/types
go test ./client
```

### 数据验证

准备两个 chunk，并保证对应 embedding 已存在：

```sql
UPDATE public.chunks
SET metadata = COALESCE(metadata, '{}'::jsonb) ||
    '{"分类":"规则","is_body":true}'::jsonb
WHERE id = '<rule-chunk-id>';

UPDATE public.chunks
SET metadata = COALESCE(metadata, '{}'::jsonb) ||
    '{"分类":"案例","is_body":true}'::jsonb
WHERE id = '<case-chunk-id>';
```

验收条件：

1. 传 `{"分类":"规则","is_body":true}` 时，只返回第一个 chunk。
2. 传 `{"分类":"案例","is_body":true}` 时，只返回第二个 chunk。
3. 传 `{"is_body":false}` 时，不返回上述两个 chunk。
4. 不传 `chunk_metadata_filter` 时，行为与修改前一致。
5. 分别关闭关键词或向量召回，确认两条路径均独立满足过滤条件。

## 已知限制与风险

- 当前只在 PostgreSQL retriever 生效；若知识库绑定其它检索后端，不能宣称为强过滤，后续应在接口层拒绝该组合或为对应后端实现同等语义。
- 本次遵循“不改变数据库字段”，也不新增 metadata GIN 索引。查询通过 `chunks.id` 主键逐候选检查 metadata，候选很多或过滤条件非常稀疏时会增加延迟。
- JSONB 匹配区分 JSON 类型；`true`、`"true"` 不等价，数字与字符串也不等价。
- chunk metadata 变更会立即影响过滤，但业务侧需要保证写入的 key 命名和类型一致。
- 上线前应对目标数据量执行 `EXPLAIN (ANALYZE, BUFFERS)`，确认 HNSW 仍被使用并记录 P95 延迟与召回数量。

## 回滚

代码回滚即可；没有数据库迁移或字段需要回滚。旧请求不传 `chunk_metadata_filter`，行为保持不变。
