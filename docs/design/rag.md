# RAG 问答技术方案（已落地）

> 目标：单轮问答 —— 答案必须带**能点回原文**的引用；没有依据时明确拒答。
> 运行时用 Eino ADK（为后续 Agent 业务扩展留位置）。
> 本文已于 2026-11-16 按 §14 落地并验收（§16），文中配置值均为实现后的实际值。
>
> **2026-11-18 架构调整**：`/ask` 已改为 ReAct 自主检索——宿主不再预检索、不再设阈值、
> 不再做引用裁决。本文 §2 的流程、§4.1 的阈值、§4.2 "为什么预检索不是 tool"、
> §10 的裁决规则均已被 `docs/design/react-agent.md` 取代；切分、检索、SSE 事件与数据表
> 仍然有效。保留本文是为了记录当时的取舍，不要按它实现。

## 1. 目标与非目标

**做**

1. `POST /api/v1/ask`：一个问题 → SSE 流式回答 → 每条论断带 `[n]` 引用 → 引用可点回文档分块。
2. 没有依据时**明确拒答**，且这个保证不依赖模型自觉（检索层与输出层各一道闸）。
3. Chat 页完整可用：输入、流式正文、引用卡片、拒答态、错误态、空态、断开。
4. 运行可追溯：每次问答落一行 `qa_runs`（问题、命中片段、最终答案、模型原文、verdict、token、耗时）。
5. 运行时用 Eino ADK 的 `ChatModelAgent + Runner`，工具化检索，为后续加工具/多轮/审批留出结构。
6. **对话模型由用户配置**：界面上增删改、存数据库、问答时选择；服务端不内置任何默认模型、不内置厂商名。

**不做（本期明确划掉）**

- 多轮对话与上下文压缩（`conversations` / `messages` 表不建）
- Rerank 模型、查询改写（multi-query / HyDE）
- 记忆召回、工具写操作、审批门、checkpoint 恢复
- 流式断点续传（事件带 `seq`，但本期不实现 `?since_seq=`）
- 中文分词器（zhparser / pg_bigm）——见 §13

## 2. 交互形态

一次问答就是一次 Agent run，HTTP 用 SSE 推事件（项目既有契约：`{"type","seq","payload"}`）。

| 事件 | 时机 | payload | 前端行为 |
|---|---|---|---|
| `retrieval` | 检索完、调模型前 | `{hits:[{n,document_id,title,seq,similarity,rrf_score,snippet}]}` | 折叠显示"本次依据"，同时把编号表建好 |
| `delta` | 模型逐 token 输出 | `{text}` | 追加到正文；此时 `[n]` 只当普通文本 |
| `verdict` | 输出结束、裁决后 | `{status:"answered"/"refused"/"ungrounded", reason}` | 决定正文是否保留；`ungrounded` 时替换成拒答并提示"上一次回答缺少依据，已撤回" |
| `citation` | 裁决为 `answered` 时 | `{items:[{n,document_id,chunk_id,title,seq,start_offset,end_offset,similarity}]}` | 把正文里的 `[n]` 变成可点 chip |
| `error` | 任何阶段失败 | `{code, message, retryable}` | 就地报错，保留已收到的正文；`retryable=false` 时不给重试按钮 |
| `done` | 收尾 | `{run_id, chat_model_id, prompt_tokens, completion_tokens, latency_ms}` | 停止 loading，写 run_id |

**三条顺序硬规则**（比字段更重要，前端的状态机就按这三条写）：

1. **`verdict` 必须在 `citation` 之前**。裁决没出来之前，没有任何引用是可信的；先发 citation 再发 verdict，一旦裁决是 `ungrounded`，前端就得把已经渲染成 chip 的引用全部撤掉，闪一下还留下错误印象。原稿的顺序是反的，这里改掉。
2. **`citation` 只在 `answered` 时发**：拒答与撤回都没有引用可点，发一个空数组只会多一种状态。
3. **`done` 一定最后发**（连接未被用户中断的前提下）。前端靠 `done` 收尾，而不是靠"流结束"——流正常关闭与网络断开在 `fetch` 里长得一样，只看流结束会把断线当成功。

各事件存在的理由，一句话一条：

- `retrieval` 不是模型产出，是**宿主的检索回执**。它让"凭什么这么答"在正文出现之前就能看，也把模型首字前那 1–3 秒填上；编号表也在这一刻建立，后续 delta 里的 `[n]` 才有意义。
- `delta` 只带 `text`，不带任何编号信息——此刻还不知道哪些引用最终有效，提前渲染只会制造无谓的 DOM 变更。**也只发 `content`，不发模型的 `reasoning_content`（思维链）**：思考过程里常出现"我需要引用 [3]"这类句子，混进正文会污染引用校验，也会让用户误以为那就是答案。
- `verdict` 是**服务端的裁决**，不是模型的自我评价。它决定正文留不留，所以必须先于引用到达。
- `citation` 与 `retrieval` 的区别要分清：`retrieval` 是"检索到 8 条候选"，`citation` 是"最终答案真正引用了其中 3 条"。只有后者带 `chunk_id` 与偏移，能跳转。
- `error` 可能在任意阶段出现；带 `retryable` 是因为"模型 429"值得重试、"未配置模型"重试一万次也一样。
- `done` 是唯一保证"这次 run 有结论"的信号，同时把 token 与耗时落给前端做展示。

**为什么流式 + 末尾裁决**：引用校验必须等完整输出才能做，但两段式（先算完再一次性返回）会让用户干等 3–10 秒。折中是"边流边看、末尾裁决"，代价是极少数情况下正文会被撤回——用一句明确提示换回速度，我认为值得。**如果评审更希望"绝不出现撤回"，改成非流式即可，其余设计不变。**

## 3. 链路

```text
POST /api/v1/ask {question, chat_model_id?}
  → chat/service：校验长度；解析用哪个模型配置（见 §7.1）；查 embedding 是否可用
  → knowledge.Search(question)                       ← 宿主发起，不是 tool
        ├ 向量路：embedding(question) → ORDER BY embedding <=> q LIMIT 30
        ├ 全文路：tsv @@ websearch_to_tsquery('simple', q) LIMIT 30
        └ RRF 融合（k=60）→ top 8
  → 阈值闸门：max(向量路 similarity) < RAG_MIN_SIMILARITY
        └ 成立 → 不调模型，verdict=refused(no_evidence)，一个 delta 都不发
  → 编号 + 注入：把 8 个片段编成 [1..8] 写入 per-run registry，
        并把片段正文拼进 user message 的 <context> 段（见 §5.2）
  → agent（Eino，按选中的模型配置构造）:
        ChatModelAgent(Instruction=引用规则, Tools=[search_knowledge])
        Runner.Query(question + context) → AsyncIterator[*AgentEvent]
        ├ MessageOutput.IsStreaming → MessageStream.Recv() → delta 事件
        └ 工具 search_knowledge 再查 → 新片段继续编号 [9..]，写入同一 registry
  → 输出校验：解析 [n] → 必须命中 registry → 否则 ungrounded
  → 落 qa_runs → verdict → （answered 时）citation → done
```

## 4. 检索（knowledge 模块）

新增 `knowledge.Search(ctx, query) ([]Hit, error)`，`Hit` 带 `document_id / chunk_id / seq / heading / content / start_offset / end_offset / similarity / rrf_score`。

```sql
-- 向量路（?1 = query 向量，?2 = 当前 embed_model）
SELECT c.id, c.document_id, c.seq, c.heading, c.content, c.start_offset, c.end_offset,
       1 - (c.embedding <=> CAST(?1 AS vector)) AS similarity
  FROM document_chunks c JOIN documents d ON d.id = c.document_id
 WHERE d.status = 'ready' AND c.embedding IS NOT NULL AND d.embed_model = ?2
 ORDER BY c.embedding <=> CAST(?1 AS vector)
 LIMIT 30;

-- 全文路（中文命中有限，见 §13）
SELECT c.id, ..., ts_rank_cd(c.tsv, q) AS rank
  FROM document_chunks c JOIN documents d ON d.id = c.document_id,
       websearch_to_tsquery('simple', ?) q
 WHERE d.status = 'ready' AND c.tsv @@ q
 ORDER BY rank DESC LIMIT 30;

-- 融合在 Go 里做：score = Σ 1/(60 + rank_i)，同一 chunk 两路命中则相加
```

四个必须写死的过滤条件：

1. `d.status = 'ready'` —— 半成品文档不参与检索。
2. `c.embedding IS NOT NULL`。
3. **`d.embed_model = 当前模型`** —— 换模型后旧向量与新查询不在同一空间，混用会得到看似合理但毫无意义的排序。这是本次向量化特意存 `embed_model` 的原因。
4. 可选 `collection` / `tag` 收窄（与列表筛选同一套条件构造器，避免两份口径）。

为什么两路都做：纯向量对错误码、版本号、函数名不敏感；纯全文对同义改写无能。中文全文在 `simple` 配置下确实弱，但保留结构，将来换分词器只改 SQL 表达式（§13）。

### 4.1 阈值只能用相似度，不能用 RRF 分数

`similarity = 1 - (embedding <=> q)`，是**余弦相似度**，归一化向量下落在 0–1。它是绝对值：0.62 就表示"这两段话方向接近"，跟本次检索里还有没有别的候选无关。

RRF 分数不是这样。`Σ 1/(60 + rank)` 只用排名算，**没有任何绝对含义**：库里只有 3 条时，第 1 名的 RRF 分与库里 3000 条时第 1 名的 RRF 分完全一样。拿它比阈值，等于拿"第一名"跟一个数字比，恒为真或恒为假。

所以闸门这样取：

| 情况 | 用哪个值判 |
|---|---|
| 向量路有命中 | `max(similarity)` 超过全部候选 |
| 向量路被 `embed_model` 过滤到空（换过模型还没重建） | 退化为"全文路是否有命中"，有则放行并记 `degraded=true` |
| 两路都空 | 直接 `refused` |

`RAG_MIN_SIMILARITY` 实测定为 0.46（起点 0.35，用真实语料校准后上调，见 §12 的校准方法与 §16 的验收数据），含义见 §12。**它只影响"答不答"，不影响"拿哪几条进上下文"**——后者由 RRF 排序 + `RAG_TOP_K` 决定，两件事分开。

**为什么"不调模型"这条要单独说**：它是这套设计里唯一能让"没有依据时不可能编造"成立的机制。只要模型被调用了，它就有机会胡说，只能靠事后校验拦；而在这里直接不调，成本是 0、概率是 0。

## 5. Agent 层（internal/agent）

Eino 版本：`github.com/cloudwego/eino` + `github.com/cloudwego/eino-ext/components/model/openai`。模型用 OpenAI 兼容组件，`ChatModelConfig{APIKey, Model, BaseURL, Timeout}` —— 四个值**全部来自数据库里的模型配置记录**（§7.1），服务端不内置任何默认模型名与地址。

```go
// 每次请求构造一套：编号必须与本次请求绑定，共享 agent 反而要把 registry 塞进 ctx。
agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name:        "mindos-rag",
    Description: "基于个人知识库回答问题，必须给出可核对的引用",
    Instruction: ragInstruction, // 见 §6
    Model:       chatModel,
    ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
        Tools: []tool.BaseTool{searchTool}, // 闭包捕获本次的 registry
    }},
})
runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
iter := runner.Query(ctx, question)
for event, ok := iter.Next(); ok; event, ok = iter.Next() { ... }
// event.Err / event.Output.MessageOutput{IsStreaming, Message, MessageStream, Role, ToolName}
// 流式读：mv.MessageStream.Recv() 直到 io.EOF（ADK 负责关闭）
```

### 5.1 检索出现两次，但只有一次是 tool

同一个 `knowledge.Search` 被两个不同的发起方调用，这是有意的：

| 发起方 | 什么时候 | 结果去哪 |
|---|---|---|
| **宿主预检索**（不是 tool） | 进 agent 之前，一次 | 过阈值闸门 + 编号 + 拼进 user message |
| **`search_knowledge` 工具** | 模型觉得不够时，0–3 次 | 返回给模型，新片段继续编号 |

只做 tool 不行，原因有三条，第一条是硬的：

1. **阈值闸门会失效**。要判断"该不该答"，必须在调模型之前知道有没有依据；如果检索是 tool，就必须先调用模型、模型再决定调不调工具——此时模型已经开口了，"没依据就不调模型"这个保证不存在了。
2. **首字延迟翻倍**。纯 tool 模式下每次问答至少两次模型往返（模型 → tool → 模型），首字从 1–3 秒变 3–6 秒。
3. **拒答会变成模型的自由裁量**。有依据但模型懒得调工具，它就会说"不知道"；这不是拒答率，是模型心情。

只做预检索也不行：模型想换个说法再查（"这个错误的成因" → "根因 排查"）就没有通道，多跳问题答不好。所以分工是：**预检索保证"有依据才开口"与首字速度，tool 保证"一次不够可以再查"**。

### 5.2 片段怎么进模型上下文

预检索到的 8 条必须**以正文形式**出现在 prompt 里，否则模型看到编号却看不到内容，只会去调工具。拼法（`<context>` 段放在 user message 里，不放进 System，避免与 Instruction 抢注意力）：

```text
<context>
[1] 文档《EasyV 多端实时共享》第 12 段（相似度 0.71）
正文前 600 字……

[2] 文档《easyv-ai 拆解》第 3 段（相似度 0.63）
正文前 600 字……
</context>

问题：EasyV 的多端共享是怎么做同步的？
```

三条约束：

- **每条标注的编号与 registry 的键完全一致**，不在 prompt 里另起一套编号。
- **单条截到 600 字**（与分块粒度一致），8 条约 5k 字符，中文约 4k token，不会挤爆预算。
- **超长时按 RRF 顺序从头截**，不随机、不按文档分组——截断本身就是一次隐式排序，得可复现。

### 5.3 工具定义

`utils.InferTool("search_knowledge", "在个人知识库中检索片段", func(ctx, *In) (string, error))`，`risk=read`。参数 `{query string, limit int}`，`limit` 上限 10；返回 JSON 片段数组（含编号与相似度）。

`internal/agent` 只依赖一个窄接口，不 import `knowledge`：

```go
// Retriever 是 agent 需要的全部检索能力。由 main 把 knowledge.Service 注入进来。
// agent 包不认识 knowledge 的类型，替换成假实现就能测工具行为。
type Retriever interface {
    Search(ctx context.Context, query string, limit int) ([]Hit, error)
}
```

这样跨模块仍是"只调 service"（AGENTS 约定），测试里也不必起数据库。

**编号与校验的归属**（本方案最关键的一条）：**编号由宿主分配，模型只能引用已存在的编号**。工具返回的片段必须先登记进 registry 才给模型看；模型输出里出现未登记的编号一律判 `ungrounded`。这样"编造引用"在结构上不可能通过校验，而不是靠 prompt 求它别编。

**预算闸门**（AGENTS 硬约束 #10）：

| 闸门 | 值 | 落点 |
|---|---|---|
| 单次模型调用 | 60s | `agent.NewChatModel` 的 HTTP Timeout |
| 工具调用 | 12 次 | 工具闭包内计数，超限返回普通工具结果让模型收尾 |
| token | 120000 | 按 `event.Output` 里的 usage 累计，超限 `adk.WithCancel()` |

不设整轮 wall-clock 超时：长回答只要单次调用和工具没有超时，就应该允许继续流式输出，不能因为整轮累计时长把正在生成的内容截断。steps/token 仍是产品约束，不配置化。

**取消**：`cancelOpt, cancelFn := adk.WithCancel()` 与 `Query` 一起传入；HTTP 连接断开（`c.Request.Context().Done()`）时调 `cancelFn(adk.WithAgentCancelMode(adk.CancelAfterToolCalls))`，等 `handle.Wait()` 收尾。断连就停，是不烧钱的唯一保证。

## 6. 引用契约与拒答

**Instruction 的硬规则**（写进 prompt，但只是第一道闸）：

1. 只使用检索到的片段回答，不得引入外部知识。
2. 每个事实性论断末尾标 `[n]`，n 必须是给定的编号；一段话多个来源写 `[1][3]`。
3. 片段不足以回答时，只输出一句"根据现有知识库无法回答该问题"，不要猜。
4. 不要复述片段原文超过 30 字，用自己的话归纳。

**服务端裁决**（第二道闸，真正起作用的）：

| 情况 | verdict | 处理 |
|---|---|---|
| 向量路 `max(similarity) < RAG_MIN_SIMILARITY` | `refused`（no_evidence） | **不调模型**，直接返回固定文案 |
| 模型输出无任何 `[n]` | `ungrounded` | 正文替换为拒答文案，保留原文进 `raw_answer` |
| 出现 registry 里没有的编号 | `ungrounded` | 同上 |
| 模型自己说无法回答（命中拒答句） | `refused` | 保留原话 |
| 有合法引用 | `answered` | 返回 citation 列表 |

`ungrounded` 与 `refused` 的区别要保留：前者是"模型编了/没给依据"（我们的系统缺陷，要盯指标），后者是"知识库里确实没有"（正常结果）。两者在页面上文案相同，但 `qa_runs.verdict` 分开记。

## 7. 数据模型（迁移 0009 `chat_models` + 0010 `qa_runs`）

### 7.1 对话模型配置（新表 `chat_models`）

先说清两类模型的定位，免得后面看混：

| 项 | 谁决定 | 存在哪 | 界面上能改吗 | 改了要做什么 |
|---|---|---|---|---|
| 向量模型（embedding） | **服务端内定** | `.env` 的 `EMBEDDING_*` | 不能 | 改配置 + 重启；换维度还要改列并全量重建向量 |
| 对话模型（chat） | **用户** | 本表 | 能，随改随用 | 什么都不用做 |

这不是取舍，是两者的约束本来就不同：向量模型的维度写进了 `document_chunks.embedding` 的列类型（`vector(1024)`），它是**数据库形状的一部分**，动它就等于改表结构；对话模型只影响一次请求的 HTTP body，改它没有任何副作用。所以这张表叫 `chat_models`，不叫 `model_configs`——一张只存一种东西的表用中性泛称，只会让人以为它还能存别的。

**服务端不内置任何默认模型**：代码里不出现厂商名、模型名、默认地址；`LLM_*` 三个环境变量从配置里删除（`IsLLMConfigured` 目前已是死代码，没有调用者）。用户通过界面添加，存库，问答时选一条。理由很直接——"哪家便宜、哪个模型中文好"一年一变，写进代码就等于每次换模型都要发版；写进 `.env` 又要求用户会改文件、还得重启。

```sql
CREATE TABLE chat_models (
    id             bigserial PRIMARY KEY,
    name           text NOT NULL,               -- 用户起的名，如「DeepSeek 对话」
    base_url       text NOT NULL,               -- 如 https://api.deepseek.com/v1
    model          text NOT NULL,               -- 如 deepseek-chat
    api_key_cipher bytea,                       -- AES-GCM 密文；NULL 表示该端点不需要 Key
    api_key_hint   text NOT NULL DEFAULT '',    -- 末 4 位，只用于界面确认"是不是这把"
    temperature    real,                        -- NULL 表示用厂商默认
    max_tokens     integer,
    is_default     boolean NOT NULL DEFAULT false,
    last_used_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX chat_models_name_uniq    ON chat_models (name);
CREATE UNIQUE INDEX chat_models_default_uniq ON chat_models (is_default) WHERE is_default;
```

**不存 `provider` 字段**：项目已定 OpenAI 兼容单协议，存了 provider 就会引出"每个 provider 一套适配"的假象。`base_url` + `model` 两个自由文本足够覆盖所有兼容端点，换厂商就是改两行字。

**解析用哪条配置**（`/ask` 不带 `chat_model_id` 时）：

1. 有 `is_default` → 用它；
2. 没有默认但只有一条 → 用它（唯一一条就是默认，不必让用户多点一次）；
3. 多条且无默认 → **400 `MODEL_NOT_SELECTED`**，让用户先选。这里绝不"取最新的那条"——静默选错模型，用户会以为是回答质量问题。
4. 一条都没有 → 503 `MODEL_NOT_CONFIGURED`。

**`is_default` 是用户的设置，不是代码的默认值**，所以它不违反"服务端不做默认配置"。

### 7.2 密钥怎么存

`config.go` 原来写着"APIKey 只从环境变量读，禁止入库、禁止进日志"。这次按你的要求破例入库，但不能裸存——`pg_dump` 一份数据库发给别人排查问题，就等于把 Key 一起发出去了。方案：

- **AES-256-GCM**（`crypto/aes` + `crypto/cipher`，标准库，无新依赖），随机 12 字节 nonce，密文格式 `nonce||ciphertext`。
- 主密钥来自 `MINDOS_MASTER_KEY`（base64 的 32 字节）。**未配置时启动自动生成**，写入 `backend/.data/master.key`（0600，已 gitignore）。不强制用户先准备一个密钥——那是给自托管团队用的门槛，不是给本地单用户的。
- 主密钥丢失 = 已存的 Key 全部解不开，只能重新填。这是加密的固有代价，写进 §13。
- **接口永远不回明文**：`GET` 只返回 `api_key_hint` 与 `api_key_configured`；`PATCH` 不传该字段表示"不改"，传空串表示"清除"（三态，与文档元信息的约定一致）。
- 日志里出现的只有 `api_key_hint`。

### 7.3 问答运行记录

去掉明文密钥之后，这张表就不该再存任何凭据，只记"这次用了哪条配置"。

```sql
CREATE TABLE qa_runs (
    id                bigserial PRIMARY KEY,
    question          text        NOT NULL,
    answer            text        NOT NULL,          -- 最终返回给用户的（拒答时是固定文案）
    raw_answer        text,                          -- 模型原始输出，便于分析 ungrounded
    verdict           text        NOT NULL,          -- answered | refused | ungrounded
    verdict_reason    text,                          -- no_evidence / no_citation / unknown_citation
    citations         jsonb       NOT NULL DEFAULT '[]',
    retrieved         jsonb       NOT NULL DEFAULT '[]',  -- 编号 + chunk_id + 两路分数
    chat_model_id   bigint,                        -- 哪条配置；不设外键，见下
    model             text,                          -- 冻结当时的模型名
    prompt_tokens     bigint      NOT NULL DEFAULT 0,
    completion_tokens bigint      NOT NULL DEFAULT 0,
    latency_ms        integer     NOT NULL DEFAULT 0,
    error_code        text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT qa_runs_verdict_check CHECK (verdict IN ('answered','refused','ungrounded'))
);
CREATE INDEX qa_runs_created_at_idx ON qa_runs (created_at DESC);
```

为什么建这张表：**拒答率是这套系统的核心指标**；`raw_answer` + `retrieved` 让"为什么拒答/为什么判 ungrounded"可复盘；token 用整数记账，为将来的成本账本留数据。

为什么 `chat_model_id` 不设外键：配置是会被删的，而历史记录不该因为删一条配置就消失或被禁止删除。同时把 `model` 名字冻结一份——配置改了名，历史记录里仍能看出当时用的是哪个模型。

为什么暂不建 `conversations` / `messages`：本期单轮，没有"会话"这个对象；提前建表只会让每条查询多一个恒为空的 join。多轮时再加，`qa_runs` 那时挂 `conversation_id`。

为什么暂不写 `tool_audits`：唯一工具是只读检索，每次调用的输入输出已经完整落在 `retrieved` 里；等出现写类工具再建（AGENTS 的留痕要求针对"能改变状态的调用"）。

## 8. 接口

### 8.1 对话模型配置（`internal/chatmodel`）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/v1/chat-models` | 列表；只返回 `api_key_hint`，永不返回明文 |
| POST | `/api/v1/chat-models` | 新增。`api_key` 必填字段名可缺省（表示该端点不需要） |
| PATCH | `/api/v1/chat-models/:id` | 改配置。**`api_key` 三态**：不传=不改，`""`=清除，有值=替换；`is_default:true` 时同事务把其它行置 false |
| DELETE | `/api/v1/chat-models/:id` | 删除；删默认时不做自动继任，让用户明确选 |
| POST | `/api/v1/chat-models/:id/test` | 真发一条 `ping`，返回 `{ok, model, latency_ms, reply}` |

`POST` 请求体：

```json
{ "name": "DeepSeek 对话", "base_url": "https://api.deepseek.com/v1",
  "model": "deepseek-chat", "api_key": "sk-...", "temperature": 0.3, "is_default": true }
```

**`base_url` 填到哪**：填到版本号为止，**不要带 `/chat/completions`**。Eino 的 OpenAI 组件底层是 go-openai，它的 `fullURL()` 就是 `TrimRight(BaseURL, "/") + "/chat/completions"`；填全路径会拼成 `.../chat/completions/chat/completions`，直接 404。几个实测填法：

| 端点 | `base_url` | `model` |
|---|---|---|
| 讯飞星辰 MaaS | `https://maas-api.cn-huabei-1.xf-yun.com/v2` | `spark-x2.5`（以控制台 Model ID 为准） |
| DeepSeek | `https://api.deepseek.com/v1` | `deepseek-chat` |
| 硅基流动 | `https://api.siliconflow.cn/v1` | 控制台里的模型 ID |

**同一家可能同时提供三种协议**，只有 Chat Completions 能用：讯飞的 `/v2/chat/completions` 是 `messages[]` 结构（这套组件支持），`/v1/responses` 是 OpenAI 新版 Responses API（`input` 结构，Eino 这个组件不支持），`/anthropic/v1/messages` 是 Anthropic 协议（同样不支持）。填错协议的表现不是报"协议不对"，而是 400 或返回空，很难查。

这些只是文档里的填写示例，**不是代码默认值**——服务端仍然一条都不内置。

`name` 全局唯一，冲突返回 409 `MODEL_NAME_CONFLICT`——重名会让提问页的下拉框出现两个无法区分的选择。

`test` 与 embedding 的探针同一个思路：配了不等于能用，Key 过期、模型名拼错、代理没放行、余额为零，只有真发一次请求才知道。它同时是**保存前的验证手段**：表单上先测通再保存，避免存了一堆用不了的配置。

### 8.2 问答

`POST /api/v1/ask`，请求 `{"question":"...","chat_model_id":3,"limit":8}`（`chat_model_id` 可省，省时按 §7.1 的顺序解析），响应 `text/event-stream`。

错误（HTTP 状态在流建立前返回）：

| 状态 | 码 | 触发 |
|---|---|---|
| 400 | `VALIDATION_FAILED` | 问题为空 / 超 500 字 |
| 400 | `MODEL_NOT_SELECTED` | 有多条配置但未指定、也没有默认 |
| 503 | `MODEL_NOT_CONFIGURED` | 一条对话模型配置都没有 |
| 503 | `EMBEDDING_NOT_CONFIGURED` | 没有向量就没有语义检索，**不退化成全文**（中文全文基本无效，退化成"假装在答"） |
| 404 | `CHAT_MODEL_NOT_FOUND` | 指定的 id 不存在 |

`qa_runs` 写失败只记 warn，不影响已经算好的回答——把一次成功的问答因为记账失败而报错，是本末倒置。

`/settings` 的 `llm` 字段改成摘要：`{"configured":true,"count":2,"default_id":3,"default_model":"deepseek-chat"}`，不再含 `api_base` / `api_key_masked`（那些在 `/chat-models` 里）。这样 Chat 页判断"有没有模型可聊"不必多打一次请求。

## 9. 前端（Chat 页 + Settings 页）

**Chat 页**

- 顶部一条轻量工具条：**模型选择下拉**（列出 `GET /chat-models`，默认选中 `is_default`，选择结果存 `localStorage`，仅影响本机偏好）+ 一个"去配置"入口（无任何配置时显示）。
- 单列布局：问题输入（多行、⌘/Ctrl+Enter 发送）→ 回答区 → 依据面板（可折叠，展示 `retrieval` 的片段与分数）→ 引用卡片。
- **SSE 用 `fetch` + `ReadableStream` 手写解析**，不用 `EventSource`：后者只支持 GET，无法带 JSON body，也无法设自定义头。这是与 `frontend/README.md` 里"用原生 EventSource"约定的**一处有意偏离**，理由写进代码注释与 README。
- 前端状态机按 §2 的三条顺序硬规则写：`verdict` 到了才渲染引用；`ungrounded` 时清空正文换成拒答；`done` 才结束 loading。
- 正文里的 `[n]` 在 `citation` 事件之后才渲染成 chip；点击跳 `/knowledge?doc=<document_id>&chunk=<seq>`。knowledge 页需要支持这两个 query 参数（当前选中态只存在内存里，这是必须补的一步），并在打开后滚动/高亮到对应分块（用 `start_offset`）。
- 状态齐全：空态（还没问过 / 还没配模型两种，文案不同）、loading（首字符前骨架）、拒答态（灰底 + "知识库里没有依据" + 建议换个问法）、错误态（按 `retryable` 决定给不给重试按钮）、断开态（"连接中断，已停止生成"）。
- 输入限制与后端一致：问题 1–500 字符。

**Settings 页新增「对话模型」区块**

- 列表：名称、`model`、`base_url`、Key 末四位、是否默认、最近使用时间。
- 新增/编辑用同一个抽屉，字段就是 §8.1 那六个；Key 输入框在编辑态显示为"已保存（****a1b2）"，留空表示不改——**不能把 hint 回填进密码框**，否则用户一保存就把真实 Key 覆盖成四个星号。
- 每行三个动作：测试、设为默认、删除（删除要二次确认，说明"已用此模型的问答记录会保留"）。
- 一条都没有时是引导态：一句话说明"问答需要至少一条对话模型配置，填 OpenAI 兼容的地址与模型名即可"，并给两个常见端点作占位提示（只是输入提示，不是默认值）。

## 10. 失败与边界

| 场景 | 行为 |
|---|---|
| 没有任何对话模型配置 | 503 `MODEL_NOT_CONFIGURED`，页面提示去 Settings 添加 |
| 有配置但没选定 | 400 `MODEL_NOT_SELECTED`，Chat 页顶部下拉高亮 |
| 未配向量模型 | 503 `EMBEDDING_NOT_CONFIGURED`，提示先配 `EMBEDDING_*` 并补齐向量 |
| 检索为空 / 低于阈值 | 不调模型，`verdict=refused`，省一次调用 |
| 模型 429 / 5xx / 超时 | 流未开始 → 502；流已开始 → 发 `error`（`retryable=true`），保留已收到的正文并标"未完成" |
| 用户关闭页面 | 取消 run（`CancelAfterToolCalls`），`qa_runs` 落 `error_code=cancelled` |
| 重复提交 | 前端禁用按钮；后端不做事前去重（单轮问答允许问两次同样的问题） |
| 引用编号越界 | `ungrounded`，正文撤回 |
| 用中的配置被删 | 本次请求已解析完 → 照常答完；下一次请求按 §7.1 重新解析（可能变 503/400） |
| 主密钥不匹配（换机器、删了 `.data/master.key`） | 解密失败 → 500 `MASTER_KEY_MISMATCH`，提示"重新填一次 Key"；**不回退成明文或空 Key**，否则会让用户以为配置还在 |

## 11. 测试与演练

不拿真实模型做脆弱测试（history 第 1 节约定 6）：

| 层次 | 覆盖 |
|---|---|
| 检索单测 | RRF 融合与去重、`embed_model` 不一致时被过滤、阈值判定（含"两路都空"与"向量路空退全文"两种）、空结果 |
| 配置单测 | 加密往返（含中文与超长 Key）、hint 只留末四位、PATCH 三态、选配置的四种分支、解不开密文时报 `MASTER_KEY_MISMATCH` 而不是空串 |
| 真库集成 | 向量路与全文路各自返回预期 chunk；`<=>` 排序正确；RRF 分数符合公式；`chat_models_default_uniq` 在并发设默认时只留一条 |
| 编排单测 | 假模型服务 + 假 embedder：正常带引用、无引用 → ungrounded、越界编号 → ungrounded、模型说"无法回答" → refused、低于阈值不调模型（并断言模型一次都没被调用） |
| 契约测试 | SSE 事件顺序与字段（**`verdict` 先于 `citation`**、`citation` 只在 answered 出现、`done` 最后）、`seq` 递增、错误事件带 `retryable`、无配置的 503 |
| 前端 | 流式渲染、引用 chip 点击跳转、拒答态、错误态（按 `retryable` 显示/隐藏重试）、断开态、模型下拉切换 |
| 故障演练 | ① 模型服务停掉 → 502/error 事件且不留半截 run；② 流中途断开 → run 标 cancelled；③ 检索库为空 → refused；④ 问题 500 字边界；⑤ 同时开两个页面 → 互不影响；⑥ 删掉 `.data/master.key` 重启 → 保存的 Key 报错可解释，不是静默失败 |

## 12. 配置

`.env` 新增 4 项（`.env.example` 同步）：

| 项 | 默认 | 说明 |
|---|---|---|
| `RAG_TOP_K` | 8 | 进上下文的片段数（1–20） |
| `RAG_MIN_SIMILARITY` | 0.46 | 低于它直接拒答；0.35 是经验起点，0.46 是真实语料校准后的值（见下） |
| `MINDOS_MASTER_KEY` | 自动生成 | base64 的 32 字节，加密模型 Key 用。留空则启动时生成到 `backend/.data/master.key` |

**删除** `LLM_API_BASE / LLM_API_KEY / LLM_MODEL`：对话模型改由数据库配置（§7.1）。若这三个变量仍有值，启动时打一条 warn 说明它们已不生效、请到 Settings 添加配置——**不做自动导入**，因为你要的就是"服务端不替用户决定用哪个模型"，静默替用户建一条默认记录与这个原则相悖。

**为什么这个进 .env、那个进数据库**，界线是"改它要不要重启、要不要重建数据"：

| 判据 | 放 .env | 放数据库 |
|---|---|---|
| 示例 | 端口、连接串、`RAG_*`、主密钥 | 对话模型配置 |
| 改了要重启 | 是（进程级参数） | 否（下次请求生效） |
| 与库形状绑定 | — | embedding 维度属于这一类，所以它留在 .env 且必须走迁移 |
| 谁来改 | 部署者 | 使用者 |

**阈值怎么定**：先跑 10 个"库里有答案"的问题与 10 个"库里没有"的问题，记录每次 `max(similarity)`。正常情况下会得到两簇：有答案的落在 0.5–0.75，没有的落在 0.2–0.35。取两簇之间最空的那一段作阈值——不是取中点，而是取"错了也不心疼"的一侧：**阈值偏高只多拒答几次，阈值偏低会让系统开始编**。0.35 是 bge-m3 中文语料的经验起点；用本库真实语料实测后定为 **0.46**（§16：有答案的问题 max ≈ 0.64–0.68，库外问题 max < 0.46 被拒答）。

校准脚本不进仓库（一次性分析），但 20 条固定问答对要留下来当回归基线（§13 第 5 条）。

## 13. 已知限制

1. **中文全文检索弱**：`tsv` 用 `simple` 配置，中文整句被当成一个词，命中率低。本期向量为主；换 zhparser/pg_bigm 需要装扩展并重建索引，触发条件是"向量路对专有名词召回不足"。
2. **引用粒度是分块**，不是句子。分块约 600 字，用户看到的引用可能比论断宽。
3. **无 rerank**：top-8 全凭 RRF，长文档多片段时容易挤在同几篇里。
4. **单轮**：追问会丢失上一轮上下文（多轮时补 conversations + ctxbuild）。
5. **无评测集**：现在只能靠人工试问，拒答率与引用准确率没有回归基线。建议下一步做 20 条固定问答对当冒烟。
6. 事件带 `seq` 但不支持断点续传，断线即结束。
7. **主密钥丢了一切重填**：`.data/master.key` 不存在（换机器、清目录）时，库里已存的 Key 无法解密。设计成报错而不是"当空 Key 用"，是因为后者更难查——请求会被厂商以 401 拒绝，而人以为是 Key 过期。要更稳就自己备份这个文件。
8. **密钥无轮换**：已按用户隔离（`chat_models.owner_id`，密文 AAD 绑定 owner），但没有密钥轮换流程；换主密钥仍需要重填所有配置。
9. **配置只在单进程内生效**：改配置无需重启，但**正在进行的 run 用的是它开始时那份**。改到一半的问答不会中途换模型，这是故意的。
10. **思维链直接丢弃**：深度思考模型（spark-x2.5 / x2.6 这类）会在 `reasoning_content` 里返回推理过程。它既不流给前端，也不落 `qa_runs`——体积大，且排查引用问题看正文就够。要复盘时临时调日志级别。
11. **模型必须支持 Function Calling**：RAG agent 靠 `search_knowledge` 工具做二次检索。选了一个不支持工具调用的模型，链路不会报错，只会永远不触发工具——表现为"多跳问题答不好"，很容易被误判成检索质量问题。选模型时先确认这一点。

## 14. 提交记录（实际）

| # | 提交 | 内容 |
|---|---|---|
| 1 | `4a435e2 feat: 实现分块混合检索与 RRF 融合` | `knowledge.Search` + `Hit` + 单测 + 真库集成 |
| 2 | `f553c6a feat: 对话模型配置落库与密钥加密` | 迁移 `0009_chat_models`、`internal/chatmodel`（AES-GCM + CRUD）、`MINDOS_MASTER_KEY` 自动生成、移除 `LLM_*` |
| 3 | `5562a25 feat: 新增问答运行记录表` | 迁移 `0010_qa_runs`、model + repository |
| 4 | `820a2cc feat: 接入 Eino ADK 并封装带工具的问答 Agent` | `internal/agent`（不认 gin、不 import knowledge，只依赖 `Retriever`） |
| 5 | `e523f2e feat: 新增流式问答接口与引用校验` | `internal/chat`（service + SSE handler）、编排、`qa_runs` 落库、httpx 错误码、路由与装配；迁移 `0011`（verdict 加 `failed`） |
| 6 | `b83b0fe feat: 问答页面前端与对话模型管理` | 计划中的第 6、7、8 条合并为一条：`pages/chat` + SSE 手写解析 + 模型下拉 + knowledge 深链 + Settings 对话模型卡片 + 全部前端用例 |
| 7 | `673669e fix: SSE 问答流清除写截止` | 真实冒烟暴露的缺陷，见 §16 |
| 8 | `docs: 记录 RAG 链路设计与验收` | 本文定稿 + overview/history/AGENTS/README |

与计划的差异：前端两条合并（同一块功能、测试随实现同提）；第 7 条 fix 是真实冒烟才发现的，方案阶段无法预见。

第 2、3 条各自一个迁移文件：两个域（配置、运行记录）没有依赖关系，合成一条提交会让"只想回滚运行记录"变成必须连带回滚配置表。

第 4、5 条分开：agent 层可以脱离 HTTP 单独测试与复用（后续 Agent 业务都从这个包长出来），chat 层负责协议与落库。混在一起，测 agent 就得起 gin。

## 15. 评审结论

| # | 决策 | 结论 |
|---|---|---|
| A | 密钥入库的加密方式 | **AES-256-GCM + 自动生成主密钥**（§7.2），已实现 |
| B | 流式 + 末尾裁决，还是非流式一次返回 | **流式 + 末尾裁决**（偶发"撤回"，换速度），已实现 |
| C | 本期是否接全文路 | **接**（成本低、结构完整；中文弱，见 §13.1） |
| D | 是否建 `qa_runs` | **建**（拒答率与可复盘是这套系统的核心指标） |
| E | 阈值与三个 `RAG_*` 配置 | 按配置实现；阈值经验起点 0.35，真实语料校准后定 **0.46** |
| F | 工具调用上限 3 次、token 上限 60k 用常量而非配置 | 按常量实现 |
| G | 现在是否就攒 20 条固定问答对做回归 | **暂不**。等真实问过一两周，把问过的挑出来当基线更有效 |

## 16. 落地验收（2026-11-16）

**假模型端到端冒烟**（本地 `/tmp/fake_chat.py`）：正常带引用、无引用判 `ungrounded` 撤回、越界编号判 `ungrounded`、模型拒答判 `model_refused`、低于阈值不调模型；`qa_runs` 落库正确。

**真实模型冒烟**（讯飞星辰 `spark-x2.6`，经界面配置加密入库）：

| 场景 | 观察 |
|---|---|
| 连通性测试 | `POST /chat-models/1/test` 返回 `{ok:true, model:"spark-x2.6", latency_ms:18357}`（推理模型冷启动偏慢，属正常） |
| 库内有答案（RabbitMQ 延迟队列） | retrieval → 46 个 delta → verdict `answered` → citation `[9][11]` 均映射到真实分块 → done；34.5s，4300/552 tokens；回答内容与文档一致 |
| 库外问题（Shor 算法） | 阈值闸门拦下：verdict `refused`/`no_evidence`，**0 token、125ms**，没有调模型 |

**验收中抓到并修复的缺陷**：`http.Server.WriteTimeout`（20s）是整条响应的硬上限，流式问答 24.5s 时写内核缓冲报 i/o timeout，客户端只收到 retrieval 事件连接即被砍。修复：`/ask` 在发响应头前用 `http.NewResponseController().SetWriteDeadline(零值)` 清掉本请求的写截止，流的存活改由单次模型调用超时与客户端断开负责；普通接口仍受全局 WriteTimeout 保护。后续已移除整轮 wall-clock 超时。

**门禁**：后端 `build` / `vet` / `test -race`（含真实 PostgreSQL/pgvector 集成）全绿；前端 `tsc` / `eslint` / 56 用例 / `build` 全绿。
