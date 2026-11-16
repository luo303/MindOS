# 分块向量化（已实现）

> 记录"切分完成之后把每个分块变成向量"这条链路的**最终形态**：为什么分成两段、事务边界在哪、失败怎么分类、界面怎么表达。
> 设计取舍与验收记录另见 `docs/history.md`；领域模型见 `docs/design/overview.md` 5.1 / 6.1。

## 1. 一句话

**导入消息只做切分；切分与"去做向量化"写在同一个事务里，向量化是第二条消息。**

```text
POST /documents
  → 事务：documents(pending) + tasks(document.import) + outbox        202 + message_id
  → relay 扫 outbox → RabbitMQ
  → 消费：读原文 → 解析 → 切分 → FinishImport（一个事务）
        ├ 替换分块、置 ready
        ├ 取消同文档未完成的 document.embed 任务（标 superseded）
        └ 配了模型？embed_status=pending + 插 tasks(document.embed) + outbox
                      : embed_status=skipped + not_configured，不排任务
  → 消费：document.embed
        ├ 读分块（id/seq/content）
        ├ 分批调 /v1/embeddings（失败整批作废，不写半份向量）
        └ 一个事务：逐行写向量 + embed_status=done + embedded_chunks=N
```

## 2. 为什么是两段任务

| 关注点 | 切分 | 向量化 |
|---|---|---|
| 依赖 | 本地 CPU | 外部模型（网络、计费、限流） |
| 耗时 | 毫秒级 | 秒到几十秒 |
| 失败后果 | 文档不可用 | 内容仍可用，只是不能语义检索 |
| 重做代价 | 换分块 id，引用失效 | 只重算向量，分块不动 |

合成一个任务看起来更简单（一次投递、没有中间态），但会让向量模型的抖动连带作废切分结果，且未配置模型时导入直接失败。拆开以后：

- 切分永不因模型问题失败；
- 换模型只需重跑向量，不必重切（重切会换掉全部分块 id，已有引用与高亮全部失效）；
- 中间态"ready 但没向量"被 `embed_status` 如实表达，而不是靠 `embedding IS NULL` 去猜。

代价是多一条消息、多一道"新鲜度闸门"（见 §3）。

## 3. 事务边界与可靠性

四层各挡一类失败：

| 层 | 挡什么 | 落点 |
|---|---|---|
| 事务 | 分块与"要向量化"不一致 | `FinishImport` 单事务；`appendTaskAndOutbox` 与业务写入同事务 |
| 投递 | relay / broker / 网络中断 | 只搬 `status=0` 的已提交行；发不出去留在表里；`ErrQueueUnavailable` 不消耗重试 |
| 执行幂等 | 至少一次投递 | `tasks.MarkRunning` 条件更新当闸门；切分是"删+插"一事务；向量写入幂等覆盖；`consumed_messages` 去重 |
| 兜底 | 消费端半路崩（消息已 ack） | `reaper` 按 `tasks` 对账重投，不看 outbox 发过没有 |

**硬约束：外部调用不进事务。** 一次 embedding 可能几十秒，而连接池只有 25 条；把它放进事务等于用一次网络等待占死一条连接。所以事务只包"写向量 + 改状态"。

**新鲜度闸门。** `document.embed` 读到分块 A，算完准备写时，用户可能已经重索引把分块换成了 B。逐行 `UPDATE ... WHERE id = ?` 只要有一行 `RowsAffected == 0`，整笔回滚并返回 `ErrStaleChunks`，**且不写 `embed_status`**。

为什么"更新到 0 行"是可靠信号：分块 id 是 `bigserial`，重索引是"删全部 + 插新的"，新旧 id 绝不复用。少了这道闸门就会出现最难查的脏数据——界面显示"已向量化"，检索却一条都返回不了。

## 4. 数据模型（迁移 0008）

```sql
-- 维度：1024，对齐 bge-m3；也在 pgvector HNSW 的上限（2000）之内
ALTER TABLE document_chunks ALTER COLUMN embedding TYPE vector(1024);
ALTER TABLE document_chunks ADD COLUMN embed_model text, ADD COLUMN embedded_at timestamptz;

ALTER TABLE documents
    ADD COLUMN embed_status        text NOT NULL DEFAULT 'pending',
    ADD COLUMN embed_model         text,        -- 产生当前向量的模型
    ADD COLUMN embed_dimensions    integer,
    ADD COLUMN embedded_chunks     integer NOT NULL DEFAULT 0,
    ADD COLUMN embed_error_code    text,
    ADD COLUMN embed_error_message text,
    ADD COLUMN embedded_at         timestamptz;

CREATE INDEX document_chunks_embedding_idx
    ON document_chunks USING hnsw (embedding vector_cosine_ops);
CREATE INDEX documents_embed_pending_idx
    ON documents (id) WHERE status = 'ready' AND embed_status <> 'done';
```

`embed_status` 五态：`pending`（待做）/ `running`（进行中）/ `done` / `failed` / `skipped`（未配置模型）。

为什么状态放 `documents` 而不是只查 `tasks`：`tasks` 回答"这次动作执行到哪"，`reaper` 会清理它；文档详情要直接回答"这份文档现在能不能检索"，所以随文档一起读出来。`skipped` 与 `pending` 必须分开——前者要用户去配置，后者只要等。

为什么 `embed_model` 单独存：它是"这批向量是否过期"的唯一判据。换模型后 `embed_status` 仍是 `done`，但 `embed_model` 与当前配置不同，批量回填据此重建。

## 5. 客户端与错误分类

`internal/platform/embedding.go`，OpenAI 兼容 `POST {base}/embeddings`，只做协议与错误分类，不认识"文档"。

| 平台哨兵 | 触发 | 上层错误码 | 是否重试 |
|---|---|---|---|
| `ErrEmbeddingUnauthorized` | 401 / 403 | `embedding_auth_failed` | 否 |
| `ErrEmbeddingRejected` | 其余 4xx（模型名错、输入过长） | `embedding_bad_request` | 否 |
| `ErrEmbeddingDimension` | 返回维度 ≠ 1024 | `embedding_dimension_mismatch` | 否 |
| `ErrEmbeddingUnavailable` | 连接失败、超时、429、5xx | `embedding_unavailable` | 是（3 次，3s/6s 退避） |
| `ErrStaleChunks` | 分块已被替换 | `stale_chunks` | 不重试、不算失败 |

客户端内部按 `EMBEDDING_BATCH_SIZE` 分批（默认 10，百炼等厂商单请求上限就是 10），响应按 `data[].index` 回填顺序（不能假设返回有序），并逐条校验维度。**客户端不做重试**：重试统一由消费端管，两层重试会让人算不清一条消息究竟调了几次模型。

## 6. 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/documents/:id/embed` | 重建某个文档的向量（不重切）。202 + message_id；未配置 503 `MODEL_NOT_CONFIGURED`；未就绪/无分块/已在进行 409 |
| POST | `/documents/embeddings/backfill` | 批量回填：`{limit}` → `{enqueued, skipped, remaining}`。候选 = ready + 有分块 + （未 done 或模型不同） |
| POST | `/settings/embedding/test` | 真调一次模型，返回 `{ok, model, dimensions, latency_ms}` 或 502 及原因 |

文档视图新增 `embedding` 对象（嵌套而不是平铺，避免与文档级 `error_*` 撞语义）：

```json
"embedding": { "status": "done", "model": "bge-m3", "dimensions": 1024,
               "chunks": 12, "embedded_at": "...", "error_code": "", "error_message": "" }
```

## 7. 前端

- 详情页两枚状态 pill 并列：文档状态、向量状态。`failed` 时就地显示原因与错误码，并说明"上次成功的 N 块向量仍可用"。
- 操作按钮随状态改文案：`done` → 重建向量、`failed` → 重试向量化、`skipped` → 生成向量；`pending`/`running` 时禁用。
- **轮询条件必须同时看两条线**：`isInFlight(doc.status) || isEmbedInFlight(doc.embedding.status)`。向量化发生在 `ready` 之后，只看文档状态会永远看不到"向量化中 → 完成"。
- 未配置模型时，知识页顶部常驻一句提示并给出去处；配置后出现「补齐向量」入口。

## 8. 配置

当前接入的是硅基流动的免费模型 `BAAI/bge-m3`（1024 维，OpenAI 兼容，需实名注册后建 Key）：

```dotenv
EMBEDDING_API_BASE=https://api.siliconflow.cn/v1
EMBEDDING_API_KEY=sk-...        # 自己的 Key，只放本地 .env
EMBEDDING_MODEL=BAAI/bge-m3
EMBEDDING_TIMEOUT=60s
EMBEDDING_BATCH_SIZE=10
```

换成通义/智谱/OpenAI 只是改这三个值；换成本机 Ollama 则是
`EMBEDDING_API_BASE=http://localhost:11434/v1` + `EMBEDDING_MODEL=bge-m3` + Key 填任意非空串。
**模型名不能漏 `BAAI/` 前缀**——硅基流动把免费版与收费版分成 `BAAI/bge-m3` 与 `Pro/BAAI/bge-m3` 两个 ID，写错会走收费。

请求体只发 `{"model","input"}` 两个字段：`encoding_format` 之类的可选项不发——兼容实现对着多余字段报 400 的情况真实存在，默认就是 float。

三项全空是合法状态（导入照常、向量化 `skipped`）；配一半会在启动时报错。

启动自检：`format_type(document_chunks.embedding)` 必须等于 `vector(1024)`，否则**拒绝启动**并说明是改迁移还是改代码常量。这类不一致会让每次写入都失败，且错误会被包成看不出原因的 `process_failed`。

**换模型**（维度不变）：改 `.env` → 重启 → 知识页点「补齐向量」，回填会挑出 `embed_model` 不一致的文档。
**换维度**：先写迁移把列改成新维度，再改 `knowledge.EmbeddingDimensions`，然后全量回填；旧向量与新向量不在同一空间，不能混用。

## 9. 已知限制

1. 未配置模型期间导入的文档只有分块、没有向量；补配置后需要点一次回填（一次最多 200 条，几十条以上要分几次）。
2. 换模型是"全量重建"，没有双写与灰度；重建期间新旧向量可能混合存在（直到回填跑完）。
3. 向量写入是逐行 UPDATE：600 块约几十毫秒。超过 2000 块/篇时需要改批量写法。
4. 只有一条队列，向量化与切分共享 `JOB_WORKERS`（默认 2）；向量化是慢 IO，可能让导入排队变长。
5. 没有成本账本：token 用量只进日志（`usage.prompt_tokens` 逐批累加）。
6. 向量 = `chunk.content` 原样，不加标题前缀。标题前缀通常能提升召回，但会改变"向量对应哪段文本"的口径，等有召回评测数据再定。
7. `reaper` 判断"是否已有待发消息"用的是 `msg_key`（文档 id），不区分任务类型：同文档的导入消息尚未发出时，卡住的向量化任务会晚一轮被重投。只影响延迟。
8. 向量化任务不提供取消：它只跑几秒，取消还要打断进行中的 HTTP 调用，而重新点一次就是重算。

## 10. 验证记录（2026-11-16）

真实链路冒烟（`:18080`，假 embedding 服务，`EMBEDDING_BATCH_SIZE=2`）：

| 场景 | 观察 |
|---|---|
| 导入笔记 | 202 → 文档 `ready` 3 块、`embed pending` → `embed done` 3 块、model/维度正确 |
| 重建向量 | 202 → `pending` → `done`，分块 id 不变（没触发重切） |
| 批量回填 | `{enqueued:10, skipped:0, remaining:14}`，陆续完成 |
| 断开模型服务 | 3 次重试后 `embedding_unavailable`，**文档仍是 `ready`**，分块可读；恢复后重试成功 |
| 连通性测试 | `{ok:true, model:"fake-bge", dimensions:1024, latency_ms:2}` |

门禁：`go build` / `go vet` / `go test -race`（含真实 PostgreSQL/pgvector 集成）全绿；前端 `tsc` / `eslint` / 44 用例 / `build` 全绿。

集成测试覆盖：`CAST(? AS vector)` 写入与 `<=>` 距离、维度校验、`ErrStaleChunks` 回滚、`FinishImport` 排任务与旧任务 `superseded`。

> 冒烟过程中发现一个真实问题：机器上残留着一个跑旧代码的 `go run` 进程，它连的是同一个 RabbitMQ 队列，抢走了消息，于是表现为"导入成功但没有向量化任务"。排查方式是对着 `tasks_id_seq` 看序列有没有被推进——序列不随事务回滚，能直接区分"没插入"与"插入后回滚"。以后做链路验证前先确认只有一个消费者在跑。
