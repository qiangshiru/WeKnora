# `POST /knowledge-bases/{id}/hybrid-search`：Chunk ID 与 Metadata 组合过滤

## 当前状态与结论

接口已增加 `chunk_ids`，并保留此前已经增加的 `chunk_metadata_filter`。二者是并列、可组合的过滤能力，`chunk_ids` 不是对 `chunk_metadata_filter` 的替换。

本次能力不需要变更数据库结构或重建索引：

- `chunk_ids` 直接过滤 `public.embeddings.chunk_id`，多个 ID 使用 OR / SQL `IN` 语义。
- `chunk_metadata_filter` 通过 `public.chunks.id = public.embeddings.chunk_id` 关联，以 JSONB `@>` 对 `public.chunks.metadata` 做包含匹配，对象中的多个字段使用 AND 语义。
- 两个字段可以分别传入，也可以同时传入；同时传入时使用 AND 语义，候选必须同时满足 ID 允许列表和 metadata 条件。

## 目标与非目标

### 目标

- 在已有 `chunk_metadata_filter` 的基础上，为 `POST /api/v1/knowledge-bases/{id}/hybrid-search` 增加可选请求字段 `chunk_ids: []string`。
- 支持一次传入一个或多个 `public.embeddings.chunk_id`。
- PostgreSQL 的向量召回与关键词召回都在排序、阈值过滤、TopK 和融合前应用该条件。
- 不传或传空数组时保持现有行为，兼容已有调用方。
- 最终返回结果仍然只包含允许列表内的 chunk。

### 非目标

- 不新增或修改 `public.embeddings`、`public.chunks` 的字段与索引。
- 不改变搜索结果结构。
- 不把 `chunk_ids` 解释为 `public.chunks.id` 后再做额外查询；过滤字段就是 `public.embeddings.chunk_id`。
- 本次只实现 PostgreSQL retriever，不顺带改造其它向量存储后端。
- 不删除、不替换或改变现有 `chunk_metadata_filter`，也不改变 `knowledge_ids`、`tag_ids` 的语义。

## 接口契约

接口现有的两项 chunk 级过滤字段：

| 字段 | 类型 | 必填 | 语义 |
| --- | --- | --- | --- |
| `chunk_ids` | `[]string` | 否 | 非空时，仅检索 `public.embeddings.chunk_id` 位于该列表中的候选；多个值为 OR / IN 关系 |
| `chunk_metadata_filter` | `object` | 否 | 非空时，要求关联的 `public.chunks.metadata` 包含全部 key/value；对象内字段为 AND 关系 |

示例：在两个指定 chunk 中，进一步限定 metadata 同时满足 `分类=规则` 和 `is_body=true`：

```json
{
  "query_text": "请说明适用规则",
  "match_count": 10,
  "vector_threshold": 0.5,
  "keyword_threshold": 0.3,
  "chunk_ids": ["chunk-00000001", "chunk-00000002"],
  "chunk_metadata_filter": {
    "分类": "规则",
    "is_body": true
  }
}
```

语义约定：

- 字段缺失或 `"chunk_ids": []`：不启用 chunk ID 过滤。
- 一个或多个 ID：使用 `IN` 语义限定候选集合。
- 重复 ID 不改变结果；实现应在边界层稳定去重，以减少 SQL 参数数量。
- 空字符串不是合法 chunk ID；在 handler 层返回 HTTP 400，不把无效值传给数据库。
- 限制为每次最多 1,000 个去重后的 ID；超限返回 HTTP 400，避免过大的 JSON 请求和 SQL 参数列表。该上限作为接口常量和测试断言固定，后续调整需显式评估。
- 列表中的 ID 不存在、已禁用，或不属于当前已授权知识库/知识条目时，不报错，只是不产生匹配结果。已有 KB 权限与 SQL 范围条件仍然生效，不能借此跨库检索。
- `chunk_metadata_filter` 缺失或为空对象时不启用 metadata 过滤；字段值保持 JSON 类型精确匹配。
- `chunk_ids` 与 `chunk_metadata_filter` 同时非空时取交集，不存在任何覆盖或替换关系。

## 数据流与过滤位置

```text
HTTP JSON chunk_ids
  -> types.SearchParams.ChunkIDs
  -> service.buildRetrievalParams
  -> types.RetrieveParams.ChunkIDs
  -> PostgreSQL KeywordsRetrieve / VectorRetrieve
  -> embeddings.chunk_id IN (...)

HTTP JSON chunk_metadata_filter
  -> types.SearchParams.ChunkMetadataFilter
  -> service.buildRetrievalParams
  -> types.RetrieveParams.ChunkMetadataFilter
  -> PostgreSQL KeywordsRetrieve / VectorRetrieve
  -> EXISTS (chunks.id = embeddings.chunk_id AND chunks.metadata @> ?::jsonb)
```

### 关键词召回

在 `KeywordsRetrieve` 构建的 GORM 条件中增加参数化 `IN`：

```sql
WHERE knowledge_base_id IN (...)
  AND chunk_id IN (...)
  AND content ||| ?
  AND (is_enabled IS NULL OR is_enabled = TRUE)
ORDER BY score DESC
LIMIT ?
```

实现应复用 GORM `clause.IN`，值通过绑定参数传入，不拼接用户提供的 ID。

### 向量召回

在 HNSW 候选子查询的 `WHERE` 中加入参数化 `IN`，位置必须在距离排序和候选 `LIMIT` 之前：

```sql
SELECT ...
FROM embeddings
WHERE dimension = ?
  AND knowledge_base_id IN (...)
  AND chunk_id IN (...)
  AND (is_enabled IS NULL OR is_enabled = TRUE)
ORDER BY embedding::halfvec(...) <=> ?::halfvec(...)
LIMIT ?
```

沿用当前显式生成 `?` 占位符并逐项追加 `allVars` 的方式，确保 ID 只作为绑定值，并保持查询向量两次占位符的现有参数顺序不变。

### 与现有条件的组合

- `chunk_ids` 内部：OR / SQL `IN`。
- `chunk_metadata_filter` 对象内部：AND / JSONB `@>`。
- `chunk_ids` 与 `chunk_metadata_filter`、KB ID、`knowledge_ids`、`tag_ids`、`is_enabled`：AND。
- 过滤必须同时进入 vector 和 keyword 两条路径，不能只在融合结果上二次裁剪，否则 TopK 可能被不允许的候选提前占用。

### 上下文扩展

当前检索结果后续可能补充父 chunk、相邻 chunk 或关系 chunk。非空 `chunk_ids` 或非空 `chunk_metadata_filter` 都表示严格过滤，因此已通过 `shouldSkipContextEnrichment` 跳过上下文扩展，避免最终响应混入不满足任一过滤条件的 chunk。

## 已完成改动

| 文件 | 已完成改动 |
| --- | --- |
| `internal/types/search.go` | `SearchParams` 新增 `ChunkIDs []string`，JSON 名为 `chunk_ids` |
| `internal/types/retriever.go` | `RetrieveParams` 新增内部字段 `ChunkIDs []string`，与现有 `ExcludeChunkIDs` 明确区分 |
| `internal/handler/knowledgebase.go` | 对 `chunk_ids` 做 trim、空值拒绝、稳定去重和最多 1,000 项校验；无效请求返回 400 |
| `internal/handler/knowledgebase_hybrid_search_test.go` | 覆盖多 ID 解析、去重、空字符串和超限拒绝，确认非法请求不会调用 service |
| `internal/application/service/knowledgebase_search.go` | 将 `SearchParams.ChunkIDs` 透传到向量与关键词 `RetrieveParams`；trace 只记录数量；非空时跳过上下文扩展 |
| `internal/application/service/knowledgebase_search_storegroup.go` | 非空 `chunk_ids` 遇到非 PostgreSQL 检索后端时返回明确的不支持错误，禁止静默忽略 |
| `internal/application/service/knowledgebase_search_metadata_filter_test.go` | 扩展上下文跳过测试，覆盖非空与空 `ChunkIDs` |
| `internal/application/service/knowledgebase_search_fanout.go` | 更新引用字段只读约定的注释，纳入 `ChunkIDs`；不改变 fan-out 算法 |
| `internal/application/service/knowledgebase_search_fanout_test.go` | 验证 fan-out / TopK 参数复制后 `ChunkIDs` 保持透传且未被修改 |
| `internal/application/repository/retriever/postgres/repository.go` | 在关键词和向量候选 SQL 中增加参数化 `chunk_id IN` 条件 |
| `internal/application/repository/retriever/postgres/chunk_id_filter_test.go` | 验证多 ID、单 ID、空列表、与其它条件组合及恶意字符串仅作为绑定参数 |
| `client/knowledgebase.go` | Go SDK `SearchParams` 新增 `ChunkIDs []string`，JSON 名为 `chunk_ids,omitempty` |
| `client` 下的请求契约测试 | 验证请求体序列化多个 `chunk_ids`，空列表省略 |
| `website-docs/04-api/02-api-knowledge.md` | 补充字段说明、严格允许列表语义和 curl 示例 |
| `docs/swagger.yaml`、`docs/swagger.json`、`docs/docs.go` | 通过 `make docs` 从注解/类型重新生成，不手工编辑生成文件 |

字符串 ID 列表规范化逻辑限定在 `hybrid-search` 范围内，没有扩大到无关模块。

## 实施记录

1. 已在 HTTP 与 SDK 的 `SearchParams` 增加 `chunk_ids`，并保留 `chunk_metadata_filter`。
2. 已在 handler 完成 `chunk_ids` 输入规范化与上限校验，并补充请求契约测试。
3. 已在 service 将两项过滤条件同时透传到 vector / keyword 参数；trace 只记录数量，不记录完整过滤值。
4. 已扩展 `shouldSkipContextEnrichment`，任一严格过滤非空时都不会追加不符合条件的上下文 chunk。
5. 已在 PostgreSQL 两条召回路径中分别加入参数化 `IN` 与 `EXISTS + JSONB @>` 条件，过滤发生在候选排序和 LIMIT 前。
6. 已补 repository、service、handler、client 各层回归测试。
7. 已更新 API 文档；Swagger 生成文件应只保留与本接口契约有关的必要差异。

## 验收标准

### 自动化验证

```bash
go test ./internal/handler -run HybridSearch
go test ./internal/application/service -run 'ChunkID|MetadataFilter|KnowledgebaseSearch|Fanout'
go test ./internal/application/repository/retriever/postgres
go test ./internal/types
go test ./client
make docs
git diff --check
```

若 `make docs` 依赖本机尚未安装的 `swag`，先执行项目已有的 `make install-swagger`；安装需要网络时单独说明，不以手工修改生成文件替代。

### 行为验收

准备同一知识库中至少三个已启用且存在 embedding 的 chunk：`c1`、`c2`、`c3`。

1. 传 `"chunk_ids":["c1"]`，向量和关键词结果都只能包含 `c1`。
2. 传 `"chunk_ids":["c1","c2"]`，结果只能来自 `c1` 或 `c2`。
3. 传 `"chunk_ids":[]` 或不传该字段，结果与修改前一致，可返回 `c3`。
4. 同时传 `knowledge_ids` / `tag_ids` 时，结果满足所有过滤条件。
5. 传不存在或属于其它 KB 的 ID，不泄露数据，只返回空或其它合法交集结果。
6. 分别关闭关键词或向量召回，确认两条检索路径都能独立执行 ID 过滤。
7. 指定父/子/相邻 chunk 之一时，响应不得因上下文扩展附带未列入 `chunk_ids` 的 chunk。
8. 传重复 ID 时行为正确，数据库实际收到去重后的列表。
9. 传空字符串或超过 1,000 个去重 ID 时返回 HTTP 400，且不执行检索。

### SQL 与性能验收

- 测试断言 SQL 使用占位符，恶意字符串不会进入 SQL 文本。
- 在目标数据量执行 `EXPLAIN (ANALYZE, BUFFERS)`，确认带少量 `chunk_ids` 时过滤发生在 HNSW 候选查询内部，并记录修改前后 P95 延迟、返回数量与执行计划。
- 如果生产场景经常传入数百至上千个 ID，应进一步评估 `ANY($1)`、临时表或 `unnest` 半连接；本次先采用与现有代码一致、可验证的参数化 `IN`。

## 已知限制与风险

- 本计划只覆盖 PostgreSQL retriever，因为需求明确针对 `public.embeddings`。对绑定 Qdrant、Milvus、Elasticsearch、OpenSearch、Weaviate、Doris 等后端的知识库，不能宣称支持相同过滤。实现时应在检测到非 PostgreSQL 后端且 `chunk_ids` 非空时返回明确的“不支持”错误，避免静默忽略过滤条件造成越权式结果。
- FAQ 检索可能使用独立索引/迭代召回流程；虽然参数会透传，仍需测试确认其 embedding 行的 `chunk_id` 与调用方所传 ID 口径一致，否则应明确拒绝 FAQ 场景。
- 大列表会增加请求体、SQL 参数和查询规划成本，因此设置 1,000 项上限；该值需要结合实际压测复审。
- `chunk_ids` 是召回范围约束，不保证列表中的每个 ID 都返回；查询阈值、启用状态、KB/knowledge/tag 过滤和 `match_count` 仍可能排除其中部分 ID。
- 当前接口仍要求有效 `query_text`，或满足已有的预计算向量特例；`chunk_ids` 本身不能替代查询条件，也不是“按 ID 直接读取 chunk”的接口。

## 回滚

仅需回滚代码、测试和文档，不涉及数据库迁移、数据回填或索引重建。旧调用方不传 `chunk_ids` 时行为保持不变。
