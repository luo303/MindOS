# 检索架构：PG + Elasticsearch + MinIO

> 状态：阶段 1（基础设施 + ES Searcher）已落地（2026-11-18）；
> indexer/回填/消息索引仍待实现（见 §11）。
> 本文取代 `docs/design/conversation-memory.md` 中"gse + tsvector"的关键词检索方案；
> 会话记忆的摘要/冷层设计仍以该文档为准，只是检索后端换成 ES。
> 核心分工：**PG 存事实，ES 存检索副本，MinIO 存原文；LLM 只通过工具间接访问。**

## 0. 一句话

`query_knowledge` 继续在 PG 上跑只读 SQL（RLS 兜底）；
`search_knowledge` 改成应用侧查 ES（BM25 + kNN + RRF）。
ES 不暴露给模型，也没有 RLS 的必要——它只是“为检索冗余出来的读模型”。

## 1. 决策与边界

| 决策 | 结论 |
|---|---|
| 事实库 | PostgreSQL：documents / document_chunks / messages / conversations / qa_runs |
| 原文 | MinIO：导入时的原始文件 |
| 检索 | Elasticsearch：分块与会话消息的读模型，负责向量 + 关键词 + 混合排序 |
| LLM 能否直接查 ES | **不能**。模型只能调 `search_knowledge` / `query_knowledge` |
| LLM 生成 SQL 的落点 | 仍然只查 PG，走只读角色 + RLS |
| ES 的租户隔离 | 代码层强制 `owner_id` 过滤，不依赖 RLS |
| 一致性 | ES 近实时，1s 延迟可接受；PG 是事实源 |
| 许可证 | Elastic License 2.0 / SSPL（若改用 OpenSearch 则为 Apache 2.0，需单独评估） |

### 1.1 为什么 ES 不需要 RLS

ES 不是模型的查询面：模型无法构造 DSL、拿不到 ES 凭据、也不能指定索引。
所有 ES 请求都由 `internal/knowledge` 的检索实现发出，签名里必须带 `ownerID`。
因此越权防护从"数据库 RLS"变成"代码层强制过滤 + 多租户测试"。

**这是一条硬约束**：任何绕过 `Searcher` 直接拼 ES 查询的代码都视为越权漏洞。

## 2. 总体架构

```text
                    ┌──────────────┐
   原始文件 ───────▶ │    MinIO     │
                    └──────────────┘
                           │ 解析/切分/embedding
                           ▼
┌──────────────────────────────────────────────┐
│ PostgreSQL（事实源）                          │
│  documents / document_chunks / messages      │
│  conversations / qa_runs                     │
│  - 事务、RLS、SQL 工具、引用校验              │
│  - 保留 chunk embedding，供 ES 重建           │
└───────────────┬──────────────────────────────┘
                │ 同事务写 outbox
                ▼
        RabbitMQ（dispatcher）
                │
                ▼
        indexer worker
                │ bulk upsert / delete
                ▼
┌──────────────────────────────────────────────┐
│ Elasticsearch（检索读模型）                    │
│  mindos-chunks-v1   ← alias mindos-chunks    │
│  mindos-messages-v1 ← alias mindos-messages  │
│  - BM25（IK 分词）                            │
│  - dense_vector kNN                           │
│  - 只给应用侧检索用，不暴露给 LLM             │
└───────────────┬──────────────────────────────┘
                │ hits（document_id/chunk_id/score）
                ▼
        knowledge.Searcher（应用侧）
                │ 回 PG 校验引用 + 组装 CitationItem
                ▼
        search_knowledge 工具 → LLM
```

## 3. 职责边界

| 组件 | 只做这些 | 绝对不做 |
|---|---|---|
| `internal/knowledge` | 文档/分块事实、ES 检索实现、索引器 | 认识 gin、拼 LLM prompt |
| `internal/agent` | 封装两个工具、ReAct 循环 | 认识 ES/PG 细节 |
| `query_knowledge` 执行器 | PG 只读 SQL + RLS | import ES 客户端 |
| `search_knowledge` | 调 `Searcher`，返回带引用的分块 | 生成 SQL、碰 PG 写路径 |
| `indexer` | 消费 outbox，bulk 写 ES | 改 PG 业务数据 |
| `cmd/server` | 装配、启停 | 业务逻辑 |

## 4. 数据模型

### 4.1 PG（事实源）

沿用现有表：

- `documents`：文档元数据；
- `document_chunks`：分块正文、偏移、embedding、embed_model；
- `messages` / `conversations`：会话与消息；
- `qa_runs`：问答审计。

**chunk embedding 必须留在 PG**：ES 重建时不需要重新调 embedding，只要从 PG 读文本和向量。
ES 是副本，不是唯一存储。

### 4.2 ES 索引

每个域一个索引，版本化 + 别名：

- `mindos-chunks-v1` / alias `mindos-chunks`
- `mindos-messages-v1` / alias `mindos-messages`

chunks 映射（关键字段）：

```json
PUT _index_template/mindos-chunks
{
  "index_patterns": ["mindos-chunks-*"],
  "template": {
    "settings": {
      "number_of_shards": 3,
      "number_of_replicas": 1,
      "refresh_interval": "1s"
    },
    "mappings": {
      "properties": {
        "chunk_id":    { "type": "long" },
        "document_id": { "type": "long" },
        "owner_id":    { "type": "long" },
        "seq":         { "type": "integer" },
        "title":       { "type": "text", "analyzer": "ik_max_word", "search_analyzer": "ik_smart" },
        "content":     { "type": "text", "analyzer": "ik_max_word", "search_analyzer": "ik_smart" },
        "heading":     { "type": "text", "analyzer": "ik_max_word" },
        "tags":        { "type": "keyword" },
        "status":      { "type": "keyword" },
        "embedding":   { "type": "dense_vector", "dims": 1024, "index": true, "similarity": "cosine" },
        "updated_at":  { "type": "date" }
      }
    }
  }
}
```

- 实现用 Elastic 官方 `smartcn` 插件（免费、版本随发行版匹配）；IK 质量略好，
  需要精确版本匹配，后续可替换，查询 DSL 不变；
- `owner_id` 用 `long`，所有查询都作为 filter；
- 建议 `routing = owner_id`，同租户数据落同分片；
- messages 索引字段：`message_id / conversation_id / owner_id / role / content / seq / embedding / created_at`。

## 5. 写入与索引

### 5.1 事件

PG 事务里写 outbox：

| 事件 | 触发 | payload |
|---|---|---|
| `search.document.upsert` | 导入、重索引、embedding 完成 | `document_id, owner_id` |
| `search.document.delete` | 删除文档 | `document_id, owner_id` |
| `search.message.upsert` | 消息落库 | `message_id, conversation_id, owner_id` |
| `search.message.delete` | 删除会话/消息 | 同上 |

- 同事务写 outbox：文档写成功但事件丢失会造成"搜不到"，outbox 消除这个窗口；
- `MsgKey = document_id` / `conversation_id`，保证同一对象的索引事件按序消费；
- payload 只放 ID，不搬运正文；indexer 从 PG 读最新数据，避免消息里是旧内容。

### 5.2 indexer

```text
消费事件
  → 按 ID 从 PG 读文档/分块（或消息）
  → 组装 _source
  → bulk index（_id = chunk_id / message_id）
  → 删除事件：delete_by_query(document_id) 或 bulk delete
  → 失败重试，超过次数进死信
```

- `_id` 用全局主键，天然幂等；重复消费只是覆盖；
- 每条 `_source` 带 `updated_at`；同一对象的乱序事件由 `MsgKey` 顺序消费兜底；
- 删除文档必须按 `document_id` 清理所有分块，避免孤儿命中。

### 5.3 重建

- 从 PG 全量扫描（不分页也可以，个人知识库量级小；大库按 `id` 游标分批）；
- `refresh=false` 批量写，写完 `_forcemerge` / 刷新；
- 新索引 `v2` 建好后切别名，旧索引保留一段时间再删；
- 重建不重新 embedding：从 PG 的 `document_chunks.embedding` 读。

## 6. 查询

### 6.1 search_knowledge：BM25 + kNN + RRF

应用侧发两个查询，各取 30 条候选，在 Go 里做 RRF（与现有实现一致，便于对比/测试）：

**关键词路**

```json
POST mindos-chunks/_search
{
  "size": 30,
  "query": {
    "bool": {
      "filter": [
        { "term": { "owner_id": 7 } },
        { "term": { "status": "ready" } }
      ],
      "must": [
        { "multi_match": { "query": "退款流程", "fields": ["title^2", "content"] } }
      ]
    }
  },
  "_source": ["chunk_id","document_id","owner_id","seq","title","content","updated_at"]
}
```

**向量路**

```json
POST mindos-chunks/_search
{
  "size": 30,
  "knn": {
    "field": "embedding",
    "query_vector": [0.01, 0.02, "..."],
    "k": 30,
    "num_candidates": 100,
    "filter": [
      { "term": { "owner_id": 7 } },
      { "term": { "status": "ready" } }
    ]
  },
  "_source": ["chunk_id","document_id","owner_id","seq","title","content","updated_at"]
}
```

**融合**

```text
score(chunk) = 1/(60 + rank_kw) + 1/(60 + rank_vec)
```

- 按 `chunk_id` 去重；
- 取 top 8；
- 不设相似度阈值（与当前无阈值架构一致）；
- 两路都空时返回空。

### 6.2 分数与引用

- kNN 的 `_score` 是 `(1 + cosine)/2`；需要展示余弦时换算 `2*score - 1`；
- 关键词命中没有余弦，`similarity = 0`，前端显示"仅字面命中"（沿用现有语义）；
- **引用回 PG 校验**：按 `chunk_id + owner_id` 从 `document_chunks` 读标题/偏移；ES 里已删除的孤儿命中直接丢弃；
- 这样即使 ES 滞后，用户点开的引用仍然来自事实库。

### 6.3 search 的租户过滤

- 所有查询必须有 `owner_id` 的 `term` filter；
- 封装在 `ESChunks.Search(ctx, ownerID, ...)` 里，禁止调用方拼 DSL；
- 集成测试固定覆盖：owner A 搜不到 owner B 的 chunk/message；
- `routing=owner_id` 只是性能优化，不是权限控制。

### 6.4 query_knowledge：不变

- 只连 PG 只读池；
- `SET LOCAL app.tenant_id` + RLS；
- 列级授权仍不含 `content`：SQL 只做统计/筛选，正文走 ES 检索。

## 7. 一致性与失败

| 场景 | 行为 |
|---|---|
| 文档刚写入，ES 还没 refresh | 1s 内可能搜不到；对话场景可接受 |
| ES 不可用 | `search_knowledge` 返回错误；`query_knowledge` 仍可用；整体降级但不丢数据 |
| indexer 积压 | 监控 lag；重试/死信；不阻塞 PG 写入 |
| PG 写成功、outbox 写失败 | 同事务，回滚；不会出现"事实有、索引没事件" |
| ES 删除失败 | 重试；引用回 PG 校验会过滤孤儿 |
| 索引 mapping 变更 | 新索引 + 别名切换 + 重建，不原地改 |

**1s 延迟是明确接受的产品取舍**：不追求"写完立刻搜到"，但要求"1s 后可见"。

## 8. 安全

- ES 不暴露公网；只允许应用网络访问；开启 TLS + 账号密码；
- ES 凭据只在 `cmd/server`/indexer 环境变量中，不进日志、不进 LLM；
- ES 存的是用户内容副本，删除/保留策略必须与 PG 一致；
- 许可证：Elastic 2.0/SSPL；若不能接受，改用 OpenSearch（API 兼容，但需单独验证 IK/客户端）；
- 多租户越权防护靠代码 + 测试，不靠 ES 的 DLS（本期不引入）。

## 9. 运维

| 项 | 建议 |
|---|---|
| 开发 | 单节点 ES，1 shard，0 replica，关闭磁盘水位 |
| 生产 | 3 节点，3 shards，1 replica，heap ≤ 50% 内存 |
| 快照 | 用 MinIO 作为 S3 兼容仓库，定期快照 |
| 监控 | 集群健康、search latency、bulk 失败、refresh lag、索引文档数 vs PG 行数 |
| 重建 | 别名切换；保留旧索引一个版本 |
| 升级 | ES 大版本 + IK 插件一起升级，先建新索引再切 |

## 10. 代码落点

| 文件 | 内容 |
|---|---|
| `internal/platform/elasticsearch.go` | ES 客户端、Ping、关闭 |
| `internal/knowledge/searcher_es.go` | `Searcher` 的 ES 实现；BM25 + kNN + RRF |
| `internal/knowledge/indexer.go` | outbox 消费、bulk upsert/delete、重建 |
| `internal/knowledge/model.go` | `Hit` 字段映射（similarity 换算） |
| `internal/chat`（阶段 3） | 冷层消息检索复用 `Searcher` 模式，查 `mindos-messages` |
| `internal/worker` | 注册 `search.*` 事件 |
| `cmd/server` | 装配 `Searcher`；按配置选择 ES/native |
| `internal/config` | `ELASTICSEARCH_*` 配置 |
| `.env.example` | 新增 ES 配置项 |

`Searcher` 接口已经存在，本次不改 `agent` 的工具契约；只换实现。

## 11. 迁移阶段

| 阶段 | 内容 | 退出条件 |
|---|---|---|
| 1 | ES 集群 + smartcn + 模板/别名（**已落地**） | 集群健康、分词验证 |
| 2 | outbox 事件 + indexer + 全量回填 | PG 行数 = ES 文档数（分块/消息） |
| 3 | 影子读：同时查 PG 原生与 ES，记录差异 | 评测集 Recall/MRR 不低于基线 |
| 4 | 切换 `search_knowledge` 到 ES | 线上错误率/延迟达标 |
| 5 | 消息索引 + 会话记忆冷层 | 长会话评测通过 |
| 6 | 下线原生 FTS（保留 embedding） | 稳定一个版本后 |

建议保留 `SEARCH_BACKEND=es|native` 开关至少一个版本，便于回滚。

## 12. 测试与评测

**单元**

- 查询构造器永远带 `owner_id` filter；
- RRF：一路空、两路重叠、同分 tie；
- `similarity` 换算；
- 引用回 PG 校验（孤儿命中被丢弃）。

**集成（ES）**

- 两个租户：A 搜不到 B；
- 删除文档后，ES 命中消失；
- indexer 重复消费幂等；
- 重建后文档数与 PG 一致；
- 中文查询（"退款""发货"）命中。

**评测**

- 50–100 个真实问题 + 期望 chunk；
- 对比 PG 原生 vs ES 的 Recall@5 / MRR / 延迟；
- 分别看关键词、向量、RRF 的贡献。

## 13. 风险与开放问题

| 风险 | 缓解 |
|---|---|
| 代码层 owner 过滤漏写 | `Searcher` 唯一入口 + 多租户测试 + review |
| ES 延迟/不可用 | 1s 延迟接受；ES 故障时 search 工具报错，SQL 工具仍可用 |
| 索引与 PG 不一致 | 定期对账（文档数/分块数）、重建脚本、孤儿引用过滤 |
| mapping/分词器变更 | 新索引 + 别名切换；不在原地改 |
| 运维/许可证 | 3 节点集群、快照到 MinIO；Elastic 2.0 评估，或 OpenSearch |
| 内容副本 | 删除同步、保留策略、ES 访问控制 |

### 13.1 落地记录（阶段 1）

- compose 增加单节点 ES + 官方 `analysis-smartcn`；`SEARCH_BACKEND=es` 时启动连接并确保索引/别名。
- `knowledge.SearcherAPI` 抽象出原生/ES 两种实现；ES 版做 BM25 + kNN + 应用侧 RRF，
  `owner_id/status/embed_model` 过滤集中在 `esFilters`。
- 单测覆盖 RRF、降级、租户过滤；后端全量测试通过。
- 未完成：ES 镜像在本机下载过慢，尚未做真实集群验证；indexer/回填、消息索引未实现。

**开放问题**

1. Elasticsearch 还是 OpenSearch？取决于许可证与 IK 插件兼容性。
2. 应用侧 RRF 还是 ES 原生 RRF？先应用侧，便于与旧实现对比；ES 8.8+ 可后续切换。
3. 消息索引保留多久？是否所有会话都索引，还是只索引已摘要的冷层？
4. ES 快照是否直接落 MinIO（已有 S3 兼容存储）？
5. 是否保留 pgvector 作为 ES 故障时的降级检索？建议保留一个版本。
