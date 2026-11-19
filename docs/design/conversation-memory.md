# 会话记忆与混合召回技术方案

> 状态：阶段 1/2/3 已落地（2026-11-19）；冷层的向量与关键词检索改走
> Elasticsearch `mindos-messages`（BM25 + kNN + 应用侧 RRF），PG 只存事实与 embedding。
> 本文是最终形态：三张表、热/冷/摘要三层、写入后异步计算 embedding、摘要只吃最新一版。

## 0. 一句话

每轮只带三样东西：**最近 16 条原文**、**一份全局滚动摘要**、**从更早原文里 RRF 召回的 5 条消息**；
摘要只吃"上一版摘要 + 新增 12 条"，冷层直接检索原文，不再有派生轮次表或段落摘要表。

## 1. 目标与非目标

**做**

1. 长会话（100+ 轮）仍能答出早期事实、决定、用户偏好；
2. 每轮上下文有硬预算，不随会话长度线性增长；
3. 摘要与 embedding 可异步、可重试、可重建；
4. 中文与英文都能做关键词召回；
5. 所有数据按 `owner_id` 隔离。

**不做**

1. 不把整段对话向量化后当成"记忆摘要"——摘要是一条压缩文本，冷层是原文；
2. 不做跨会话记忆（用户级长期记忆），本期冷层只查当前会话；
3. 不引入第二套向量库；冷层检索使用 Elasticsearch 读模型，PG 仍保留 embedding 供重建；
4. 不做消息编辑/删除后的增量修正（第一期不支持编辑）。

## 2. 三层与预算

| 层 | 内容 | 读取方式 | 预算 |
|---|---|---|---|
| 热 | 最近 16 条原文 | 每轮都带，作为对话消息 | ≈4k token |
| 冷 | 比热层更早的原文消息 | 向量 + 关键词 + RRF，top 5 | ≈1.5k token |
| 摘要 | 一份全局压缩，最新一版 | 每轮都带 | ≤0.8k token |

### 2.1 热层边界

设最后一条消息的 `seq` 为 `last`：

```text
hotStart = max(1, last - 16 + 1)      // 最近 16 条
热层：seq >= hotStart
冷层：seq <  hotStart
```

如果热层 token 超过 4k，把 `hotStart` 继续往前推（减少热层条数），直到进入预算。
**冷层不依赖 `summarized_until_seq`**，所以摘要任务滞后也不会出现"既不在热层、也不在冷层"的空洞。

### 2.2 每轮上下文预算

```text
system（指令 + 工具说明）        1.5k
摘要                             0.8k
冷层（top 5 + 相邻消息）          1.5k
热层原文                         4.0k
当前问题                         0.5k
预留输出                         2.0k
------------------------------------
合计                            10.3k
```

按估算 token 计，留 20% 余量；实际以模型的 usage 校准。

## 3. 数据模型

三张表，各司其职：

- `conversations`：会话列表与摘要水位；
- `messages`：所有原文消息，一条一行，`role` 区分 user/assistant；
- `conversation_summaries`：滚动摘要，按版本存。

```sql
CREATE TABLE conversations (
    id                   bigserial PRIMARY KEY,
    owner_id             bigint NOT NULL REFERENCES users(id),
    title                text   NOT NULL DEFAULT '',
    message_count        integer NOT NULL DEFAULT 0,
    last_message_at      timestamptz,
    -- 摘要已经覆盖到哪一条（1..summarized_until_seq）
    summarized_until_seq integer NOT NULL DEFAULT 0,
    -- 最新摘要版本，指向 conversation_summaries.version
    summary_version      integer NOT NULL DEFAULT 0,
    -- 摘要任务的认领标记，防止两个 worker 同时做
    summary_claimed_until integer NOT NULL DEFAULT 0,
    summary_claimed_at    timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversations_owner_idx ON conversations (owner_id, last_message_at DESC);

CREATE TABLE messages (
    id              bigserial PRIMARY KEY,
    conversation_id bigint NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    owner_id        bigint NOT NULL REFERENCES users(id),
    role            text   NOT NULL CHECK (role IN ('user', 'assistant')),
    content         text   NOT NULL,
    citations       jsonb  NOT NULL DEFAULT '[]',
    seq             integer NOT NULL,          -- 会话内从 1 开始
    -- 冷层语义检索；写入后异步计算，失败时为 NULL
    embedding       vector(1024),
    embedding_model text,
    embedded_at     timestamptz,
    -- 热层/摘要预算的启发式估算
    token_count     integer NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq)
);
CREATE INDEX messages_conv_seq_idx ON messages (conversation_id, seq);
CREATE INDEX messages_owner_idx ON messages (owner_id, conversation_id, seq);
-- 关键词/向量检索在 ES 的 mindos-messages；PG 不建 tsvector / HNSW。
-- embedding 列保留，用于 ES 重建时不必重新调用向量模型。

CREATE TABLE conversation_summaries (
    id                bigserial PRIMARY KEY,
    conversation_id   bigint NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    owner_id          bigint NOT NULL REFERENCES users(id),
    version           integer NOT NULL,
    -- 这一版摘要覆盖的范围（累计，from 恒为 1）
    covered_from_seq  integer NOT NULL DEFAULT 1,
    covered_to_seq    integer NOT NULL,
    -- 这一版新增输入的范围（用于审计）
    batch_from_seq    integer NOT NULL,
    batch_to_seq      integer NOT NULL,
    content           text   NOT NULL,
    token_count       integer NOT NULL,
    model             text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, version)
);
CREATE INDEX conversation_summaries_latest_idx
    ON conversation_summaries (conversation_id, version DESC);
```

### 3.1 为什么是三张表

| 数据 | 表 | 原因 |
|---|---|---|
| 会话元数据 | `conversations` | 列表、标题、摘要水位、认领状态 |
| 原文消息 | `messages` | 追加写、按 `seq` 读、向量/关键词索引 |
| 摘要 | `conversation_summaries` | 版本化、可审计/回滚、只读最新 |

不建 `conversation_turns`：命中单条消息后按 `seq±1` 补相邻消息即可还原一问一答，
不需要派生表、同步逻辑和额外索引。

不把摘要放 `conversations` 里：摘要要保留版本、内容较长、更新频繁；单独表更清晰。

## 4. 写入路径

### 4.1 消息落库

assistant 回复完成后，在一个事务里：

1. `INSERT messages`（`seq = message_count + 1`，更新 `conversations.message_count / last_message_at`）；
2. 同一事务写 `search.message.upsert` 与 `search.message.embed` 事件；
3. 提交。

### 4.2 embedding 写入时计算

消息落库后立即计算 embedding，**不等到变冷**：

- 同一轮的 user + assistant 可以合并成一次批量请求（embedding 接口支持 batch）；
- 先落库、再算向量，不阻塞 SSE 首字；
- 成功：`UPDATE messages SET embedding=$, embedding_model=$, embedded_at=now()`；
- 失败：`embedding` 保持 NULL，记录 `embed_error`（或日志）；关键词路仍可用，后台重试补齐。

不把 embedding 放进摘要任务：摘要按"积压 12 条"触发，可能比消息晚很多；
写入时算可以让冷层在消息变冷时已经具备语义检索能力。

### 4.3 token 估算

Go 侧启发式：

```text
estimateTokens(s) ≈ CJK 字符数 × 1 + 非 CJK 字符数 / 4
```

存储到 `messages.token_count`，用于热层预算与摘要输入预算；实际 usage 用于校准。

## 5. 热层

```text
hot = messages
      WHERE conversation_id = $1 AND owner_id = $2 AND seq >= hotStart
      ORDER BY seq ASC
```

- 默认 16 条；如果超过 4k token，从最老的开始丢，直到进入预算；
- 热层消息按原样作为 Eino 的 `user` / `assistant` 消息传入，不拼成字符串；
- 热层不依赖摘要是否完成，保证最近对话永远在。

## 6. 滚动摘要

### 6.1 触发

assistant 消息落库后：

```text
backlog = hotStart - summarized_until_seq - 1
if backlog >= 12:
    取最老的 12 条未摘要消息（summarized_until+1 .. +12）
    入队 conversation.summarize(conversation_id)
```

- 不是每轮都摘要，积压 12 条才做一次；
- 摘要可以滞后于 `hotStart`：中间那段由冷层覆盖，不会丢；
- 如果会话很短（`hotStart` 之前不足 12 条），不做摘要。

### 6.2 认领（短事务，不调模型）

```sql
BEGIN;
SELECT id, summarized_until_seq, summary_version,
       summary_claimed_until, summary_claimed_at
  FROM conversations
 WHERE id = $1 AND owner_id = $2
   FOR UPDATE;

-- 有未过期认领（5 分钟内）→ 放弃
-- 过期认领 → 可以抢占

UPDATE conversations
   SET summary_claimed_until = $batch_to,
       summary_claimed_at = now()
 WHERE id = $1;
COMMIT;
```

### 6.3 调模型（事务外）

输入只有两样：

1. **上一版最新摘要**（`conversation_summaries` 中 `version = conversations.summary_version` 的那一条）；
2. 新增的 12 条消息。

```text
new_summary = summarize(latest_summary.content, messages[batch_from..batch_to])
```

- 不读更早的摘要版本；第 N 版已经包含 1..covered_to，滚雪球；
- 如果还没有摘要（version=0），只输入这 12 条；
- 输出 ≤800 token，只保留：目标/约束、用户偏好、已确认决定、未解决问题、关键实体与数字；
- 丢弃寒暄、重复确认、过程性解释。

### 6.4 提交（短事务）

```sql
BEGIN;
SELECT ... FOR UPDATE;
-- 校验 summary_claimed_until = $batch_to
-- 且 summarized_until_seq < $batch_to，否则放弃

INSERT INTO conversation_summaries
  (conversation_id, owner_id, version, covered_from_seq, covered_to_seq,
   batch_from_seq, batch_to_seq, content, token_count, model)
VALUES ($conv, $owner, $version, 1, $batch_to,
        $batch_from, $batch_to, $summary, $tokens, $model);

UPDATE conversations
   SET summarized_until_seq = $batch_to,
       summary_version = $version,
       summary_claimed_until = 0,
       summary_claimed_at = NULL
 WHERE id = $conv;
COMMIT;
```

失败时只清认领：

```sql
UPDATE conversations
   SET summary_claimed_until = 0, summary_claimed_at = NULL
 WHERE id = $1 AND summary_claimed_until = $batch_to;
```

### 6.5 读取最新摘要

```sql
SELECT content
  FROM conversation_summaries
 WHERE conversation_id = $1 AND owner_id = $2
 ORDER BY version DESC
 LIMIT 1;
```

或者用 `conversations.summary_version` 直接定位。旧版本只用于审计/回滚，不参与生成。

### 6.6 漂移与重建

"旧摘要 + 新消息"会累积漂移。控制手段：

- 冷层保留**全部原文**，细节永远可从原文召回；
- 摘要保留版本；质量下降时从 `messages` 重新生成，`version` 继续递增；
- 重建不依赖旧摘要：可以把 1..N 的消息分批摘要后合并，或按固定窗口重新生成。

## 7. 冷层：原文双路召回

### 7.1 范围

所有冷层查询都带三条过滤：

~~~text
owner_id = $owner
conversation_id = $conv
seq < $hotStart
~~~

- 只查当前会话（本期不做跨会话）；
- 只查比热层更早的消息；
- 向量路只召回 `embedding` 非空的消息；失败为 NULL 的只走关键词路。

### 7.2 语义路

ES 的 kNN 查询（过滤条件同时下推）：

~~~json
{
  "size": 20,
  "knn": {
    "field": "embedding",
    "query_vector": [ ... ],
    "k": 20,
    "num_candidates": 100,
    "filter": [
      { "term": { "owner_id": 1 } },
      { "term": { "conversation_id": 7 } },
      { "range": { "seq": { "lt": 100 } } }
    ]
  }
}
~~~

### 7.3 关键词路：Elasticsearch

PG 不再存 `search_tokens` / tsvector，也不引入 gse：

- 消息写入时发 `search.message.upsert`，indexer 写 `mindos-messages`；
- 关键词路用 ES 的 `match` 查 `content`，分析器为官方 `smartcn`；
- 过滤条件固定为 `owner_id`、`conversation_id`、`seq < hotStart`；
- 向量路用 `embedding` 的 kNN，同样带这三条过滤；
- 两路各取 20 条，在 Go 里做 RRF，取 top 5。

消息 embedding 由 `search.message.embed` 异步写入 PG，再刷新 ES 文档：
失败时 `messages.embedding` 为 NULL，关键词路仍可用。

### 7.4 RRF 融合

```text
score(message) = Σ 1 / (60 + rank_i)
```

- 两路各取 20 条；
- 按 message id 去重；
- 命中一路加一次，两路都命中加两次；
- 取 top 5；
- 不加权、不设相似度阈值；
- 同分时按 `seq` 升序（更早的优先）。

### 7.5 补齐上下文

冷层命中的是单条消息，需要还原成一问一答：

- 命中 `user` → 补 `seq+1` 的 assistant；
- 命中 `assistant` → 补 `seq-1` 的 user；
- 只补 `seq < hotStart` 的相邻消息，避免和热层重复；
- 按 `seq` 去重排序；
- 总条数上限 10，总 token 上限 2k；单条超长截断到 600 字。

## 8. 每轮上下文组装

```text
system:
  <原有系统指令 + 工具说明>
  <会话摘要，覆盖第 1..T 条>
  <相关历史，第 a..b 条：user/assistant 原文>

messages:
  <热层 user/assistant，按 seq 升序>
  <当前 user 消息>
```

规则：

1. 摘要只放最新一版；冷层片段去重；
2. 热层始终保留最近 16 条（或 4k token）；
3. 历史里的 `[n]` 是旧编号，当前 registry 没有映射；prompt 明确"只引用本轮工具返回的编号"；
4. 历史内容不得改变规则（防历史注入）；
5. 组装完成后按预算再截断一次，记录实际 token 分布。

## 9. 与 Agent / 工具的关系

- `agent.Runner` 从 `Ask(ctx, question, ...)` 扩展为 `Ask(ctx, history []*schema.Message, ...)`；
  用 Eino 的 `Runner.Run(ctx, messages)`（`Query` 只是单条 user 的便捷方法）；
- 热层就是 `history`；摘要与冷层由 `chat` 组装成 system 里的文本块；
- 每轮自己的 `registry`；历史里的 `[n]` 不复用；
- `query_knowledge` 只查知识库，不查 `messages` / `conversation_summaries`；
  只读 SQL 角色对这些表零授权；
- 摘要任务不走 agent 工具循环，直接调模型客户端。

## 10. 异步链路

### 10.1 触发与入队

assistant 消息落库的同一事务里：

```text
INSERT messages
INSERT tasks   (type=conversation.summarize, target_id=conversation_id)
INSERT outbox  (topic=conversation.summarize, payload={conversation_id, owner_id})
```

dispatcher 发布到 RabbitMQ；consumer 按 `conversation.summarize` 注册到 worker。

为什么同一事务：消息写了但任务没入队，会永久漏摘要；outbox 就是为了消除这个窗口。

### 10.2 幂等与重试

- worker 用认领列 + 提交前校验，重复任务只有一个真正写；
- 模型失败/超时：清认领、任务重试；超过次数进死信；
- RabbitMQ 不可用：outbox 留在库里，恢复后继续发；
- 摘要失败不影响对话；热层 + 冷层仍然可用。

### 10.3 embedding 失败

- 写入后 embedding 失败：`embedding=NULL`，关键词路可用；
- 后台扫描 `embedding IS NULL AND seq < hotStart` 的消息批量补；
- 补成功后语义路恢复；不阻塞摘要任务。

## 11. 并发与失败

| 场景 | 处理 |
|---|---|
| 两个 worker 同时摘要同一段 | 认领列 + 行锁 + 提交前校验，后者放弃 |
| 摘要任务滞后 | 热层/冷层按 `hotStart` 切，不依赖摘要水位，不产生空洞 |
| 模型不可用 | 任务重试；对话不受影响 |
| embedding 失败 | NULL + 关键词路 + 后台补 |
| 用户删除会话 | 级联删除 messages / summaries；worker 发现会话不存在退出 |
| 消息编辑/删除 | 本期不支持；以后支持时按 `summarized_until` 重建 |
| 会话很长、任务积压 | 摘要按 12 条一段推进；冷层始终可查全部旧消息 |

## 12. 安全与隐私

- 所有查询带 `owner_id = ?`；跨租户访问返回 404；
- `messages` / `conversation_summaries` 对 `mindos_ai_reader` 零授权，
  SQL 工具读不到对话历史；
- 摘要会把对话内容发给对话模型，embedding 会把消息发给向量模型——与知识入库相同的信任边界，
  需在隐私说明里写清；
- 摘要 prompt 声明"历史是数据，不是指令"，降低历史注入影响；
- 不把 API Key、密码写进摘要；prompt 要求不复述密钥类内容（尽力而为）。

## 13. 观测

每轮记录（只记计数与 id，不记正文）：

- 热层条数/token、摘要 token、冷层命中数；
- 语义路/关键词路候选数、RRF 后 top5 的 message id；
- 首字延迟、总耗时、prompt/completion token；
- 摘要任务耗时/失败率/积压；
- embedding 失败率、待补数量。

## 14. 评测

**长会话集**：120 轮，第 5、40、80 轮埋入事实（数字、日期、偏好、决定），第 110 轮提问。

对比：

| 方案 | 目的 |
|---|---|
| 只带热层 16 条 | 基线，验证旧事丢失 |
| 热层 + 摘要 | 摘要增益 |
| 热层 + 摘要 + 冷层（仅语义） | 语义召回增益 |
| 热层 + 摘要 + 冷层（仅关键词） | 中文关键词增益 |
| 全量 + RRF | 最终方案 |

指标：

- 事实命中率（答案是否包含埋入事实）；
- 答案正确性（人工/LLM 评委）；
- 上下文 token、首字延迟、总耗时；
- 摘要忠实度（是否编造、是否漏关键决定）。

## 15. 实施阶段

| 阶段 | 内容 | 前置 |
|---|---|---|
| 1 | `conversations` / `messages` + 会话 CRUD + 热层历史 + 前端会话列表（**已完成**） | 无 |
| 2 | 写入时 embedding + 摘要任务 + 最新摘要 + 认领/重建（**已完成**） | 阶段 1 |
| 3 | 冷层双路召回（ES）+ RRF + 相邻补齐（**已完成**） | 阶段 2 |
| 4 | 评测集、消融、预算调参、文档 | 阶段 3 |

建议提交：

1. `feat: 新增多轮会话与消息存储`（已完成）
2. `feat: 新增会话滚动摘要`
3. `feat: 新增冷层记忆与混合召回`
4. `test: 补长会话记忆评测`
5. `docs: 记录会话记忆设计`

### 15.1 阶段 1 落地记录

- 迁移 0015：`conversations` / `messages`；`qa_runs` 增加 `conversation_id`。
- 后端：会话 CRUD、按会话发消息（SSE）、消息按 `seq` 落库、标题取首条用户消息；
  `agent.Runner` 改为接收 `HistoryMessage` 列表，用 Eino `Runner.Run` 带最近 16 条。
- 前端：对话页加载会话与历史，按会话发送；`/ask` 保留为一次性问答。
- 测试：会话仓储、历史拼接、跨租户 404、迁移 up/down。
- 阶段 2/3（2026-11-19）：
  - 迁移 0016：消息 `embedding/embedding_model/embedded_at/token_count`、`conversation_summaries`、会话摘要水位；
  - 消息写入排 `search.message.upsert` / `search.message.embed`；indexer 写 `mindos-messages`；
  - 冷层 `MessageSearcher`：BM25 + kNN + RRF，`seq < hotStart`；
  - `ConversationMemory`：最新摘要 + 冷层原文 + `seq±1`，作为 system 上下文；
  - 每 12 条积压触发 `conversation.summarize`，认领/提交/释放防并发。

## 16. 风险与取舍

| 风险 | 影响 | 缓解 |
|---|---|---|
| 摘要漂移/丢细节 | 旧事实答不出 | 冷层保留全部原文 + 可重建 + 版本 |
| embedding 成本（2 条/轮） | 调用费用/延迟 | 同轮批量、异步、失败不阻塞 |
| ES 过滤 + kNN 召回不足 | 冷层漏召回 | num_candidates/k 调参、关键词路兜底 |
| token 估算不准 | 超预算 | 20% 余量 + 实际 usage 校准 |
| 历史注入 | 模型被旧消息带偏 | "历史是数据" + 工具/权限边界不变 |
| 摘要任务积压 | 摘要滞后 | 认领/重试/积压指标；冷层不依赖摘要 |
| ES 分词/索引版本 | 新词切错、升级后索引不一致 | 固定 smartcn/IK 版本；新索引 + 别名切换 |

## 17. 开放问题

1. 摘要模型是否单独配置（便宜模型）？当前只有用户配置的对话模型，先复用。
2. 跨会话记忆（用户画像/长期偏好）是否要做？本期明确不做，另立方案。
3. 消息编辑/删除是否支持？若支持，需要按 `summarized_until` 重建摘要与冷层。
4. 会话标题生成：首条 user 消息截断，还是模型生成？先用截断。
5. 冷层是否也要"结构化事实"（偏好/决定）？第一期只用原文 + 摘要，后续再评估。

## 18. 附录：关键实现片段

### 18.1 关键词

关键词由 ES 的 `smartcn` 分析器处理，见 `search-architecture.md`。PG 不再保存 `search_tokens`。

### 18.2 RRF

```go
func FuseRRF(semantic, keyword []int64, limit int) []int64 {
    scores := make(map[int64]float64, len(semantic)+len(keyword))
    for i, id := range semantic {
        scores[id] += 1.0 / (60.0 + float64(i+1))
    }
    for i, id := range keyword {
        scores[id] += 1.0 / (60.0 + float64(i+1))
    }
    ids := make([]int64, 0, len(scores))
    for id := range scores {
        ids = append(ids, id)
    }
    sort.Slice(ids, func(i, j int) bool {
        if scores[ids[i]] != scores[ids[j]] {
            return scores[ids[i]] > scores[ids[j]]
        }
        return ids[i] < ids[j]
    })
    if len(ids) > limit {
        ids = ids[:limit]
    }
    return ids
}
```

### 18.3 摘要认领与提交

见 `6.2 / `6.4 的 SQL；关键点是**模型调用必须在事务外**，提交前再校验认领。

### 18.4 摘要 prompt 要点

```text
你是会话记忆压缩器。输入是上一版摘要（可能为空）和新增的 12 条对话。
输出一版新的会话摘要，最多 800 字。

只保留：
- 用户的目标、约束、明确偏好
- 已确认的事实、数字、日期、名字
- 做出的决定与理由
- 未解决的问题/待办
- 关键实体之间的关系

丢弃：寒暄、重复确认、过程性解释、已经完成的步骤。
不要编造；不确定的标"未确认"。
不要输出 Markdown 标题。
```
