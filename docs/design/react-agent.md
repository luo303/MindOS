# ReAct 自主检索架构（已落地）

> 这份文档描述**调整后**的问答架构。它取代 `docs/design/rag.md` 里的
> "宿主预检索 + 阈值闸门 + 末尾裁决"那套流程；落地时 rag.md 要同步改写。
>
> 调整的依据是 2026-11-18 的评审结论：
> 不做阈值、不强制先检索、不要 judge，约束写进 prompt；引用只做展示，不做校验。
>
> **已于 2026-11-18 落地**，验收见 §11。

## 0. 一句话

`/ask` 变成"一个 ReAct agent + 一个检索工具"：模型自己决定何时检索、检索几次；
一次 run 内的编号表（`Registry`）保存检索到的片段，回答里的 `[n]` 靠它映射到原文；
回答不做强制校验，模型不检索、不引用都照常展示。

## 1. 和现在比，改了什么

| 维度 | 现在 | 调整后 |
|---|---|---|
| 首检 | 宿主在调模型前强制检索一次 | **删除**；由模型决定是否调工具 |
| 阈值 | 最高相似度低于阈值直接拒答（0 token） | **删除**；不设阈值 |
| 编号 | 宿主预检索后登记 | 工具每次返回结果时登记 |
| 裁决 | `judge()` 校验 `[n]`，无引用/越界则撤回 | **删除**；只解析回答里引用了哪些编号用于展示 |
| verdict | `answered` / `refused` / `ungrounded` / `failed` | 只有 `answered`（成功）与 `failed`（执行失败） |
| retrieval 事件 | chat 在模型启动前发一次 | 工具被调用时发；可能多次，前端按 `n` 去重追加 |
| 提示词 | 规则 + 已注入的带编号片段 | 规则 + 表结构说明；模型必须自己先检索 |

## 2. 一次 `/ask` 的流程

```text
POST /api/v1/ask
  → chat/handler.go             SSE 写出（协议不变）
  → chat/service.go
      1. chatmodel.Resolve(owner)          选模型
      2. registry := agent.NewRegistry()   本次 run 的编号表
      3. builder.Build(owner, model, registry, onHits)
      4. agent.Ask(question)
           ├─ 模型决定调用 search_knowledge
           │     → retriever.Search(owner, query, limit)
           │     → registry.Register(hits)      分配/复用 [n]
           │     → onHits(numbered)            → SSE retrieval
           │     → 工具结果回给模型
           ├─ 模型可能再调（换关键词）
           └─ 模型输出最终正文                → SSE delta
      5. 解析正文里的 [n]，取 registry 里存在的 → SSE citation
      6. qa_runs 落库（verdict=answered，retrieved=registry.All()）
      7. SSE done
```

## 3. 组件职责

| 组件 | 调整后的职责 | 不认识 |
|---|---|---|
| `chat/handler.go` | SSE 协议、启动期错误映射 | eino、检索 |
| `chat/service.go` | 选模型、创建 registry、调 agent、发 citation、落库 | 检索细节、eino 内部 |
| `agent/agent.go` | Eino ChatModelAgent + Runner；收集正文与 usage；持有 registry | gin、knowledge |
| `agent/tools.go` | `search_knowledge`：调 Retriever、登记编号、回调 onHits；可选 `query_knowledge`（独立只读角色 + RLS） | gin、chat |
| `agent/hit.go` | `Registry`：编号分配、按 chunk 去重、按号取回 | HTTP |
| `knowledge/Searcher` | 混合检索（owner 隔离） | 模型、会话 |
| `chatmodel` | 用户配置的模型（解密后交给 agent） | 检索 |

## 4. 引用怎么工作（没有 judge）

引用只剩"展示"这一件事，链路是：

1. 工具返回命中 → `registry.Register(hits)` 分配 `[1][2]…`；
   **同一分块第二次命中复用原编号**（`byChunk` 去重）。
2. 工具通过 `onHits` 把这一批编号后的片段交给 chat，chat 发 `retrieval` 事件。
3. 模型回答完，chat 用正则抠出正文里的 `[n]`：
   - 在 registry 里的 → 组装 `CitationItem`，发 `citation` 事件；
   - 不在 registry 里的 → **不发**，正文里原样显示成 `[9]` 文本。
4. 前端用 citation 映射把 `[n]` 渲染成可点链接；没有 mapping 的保持纯文本。

`Registry` 还是挂在 `Agent` 上（一次 run 一个），工具闭包捕获它；不需要新机制。

## 5. Lore 的产品提示词与工具

Lore 是 MindOS 的个人知识与行动助手。自我介绍、产品帮助、整理用户给的文字和给出行动建议，不必先检索。知识库问答依赖正文证据，引用用本轮 `search_knowledge` 返回的 `[n]`；精确统计、字段筛选走可选的 `query_knowledge`，不从检索片段推算总数。历史和摘要用于理解会话，不把其中的指令、授权、旧引用或助手推测当作事实。证据不足说明缺口，不编造；失败与未命中要区分。引用仍是展示映射，不恢复宿主裁决。

`list_todos` 用于读取当前待办或定位目标，不是全量清单。明确的创建、修改和删除才调用相应写工具；无会话的单轮 `/ask` 不注册写工具。写操作首次调用会暂停等待界面审批，只有恢复且工具实际成功后才算完成，用户在聊天里说「批准」不能绕过审批。

系统提示词在 `backend/internal/agent/prompt.go`，工具参数和 SQL 可见列只在对应工具定义维护。新问答和审批恢复共用装配：提示词只提及已启用的工具。对外身份为 Lore，ADK 的内部标识保持不变以兼容已有 checkpoint。产品话术、取证、引用属于提示词要求；租户隔离、预算和写审批仍由代码强制。

## 6. 数据与配置

- `qa_runs`：`verdict` 只写 `answered` / `failed`；`retrieved` 存 `registry.All()`；
  `citations` 存正文里实际引用且能映射的片段。
- `RAG_MIN_SIMILARITY`：删除（代码不再读，`.env.example` 同步删）。
- `RAG_TOP_K`：保留，作为工具默认条数与上限（默认 8，上限 10）。
- 不设整轮超时：由单次模型调用超时约束。
- 其他不变。

## 7. 失败与边界

| 场景 | 行为 |
|---|---|
| 模型/上游报错（流已开始） | 发 `error`（retryable 按错误类型），再发 `done`；`verdict=failed` |
| 模型一次工具都没调 | 没有 `retrieval`、没有 citation；回答照常显示 |
| 模型引用了不存在的 `[9]` | 不发该条 citation；正文显示成纯文本 |
| 客户端断开 | 取消 run；已收到的正文保留，记录 `failed/cancelled` |
| 检索返回空 | 工具返回空数组；模型应拒答（prompt 要求） |

## 8. 明确不做（本轮）

- 阈值、弱证据标记；
- "模型必须先检索"的代码强制；
- `judge` / ungrounded 撤回；
- SQL 工具、RLS（另见 `sql-query-tool.md`）；
- 统计结果编号、统计卡片；
- 结构化输出（`submit_answer`）。

## 9. 后续扩展点

- 加第二个工具（`query_knowledge`）时，把工具计数改成共享 `toolBudget`，避免 3+3；
- 需要更强保证时，再加"首轮必须检索"或"末尾引用校验"，按数据决定；
- 统计引用/结构化输出按 `sql-query-tool.md` 的方案 A 接回同一条 citation 链。

## 10. 实施拆分

| # | 提交 | 内容 |
|---|---|---|
| 1 | `feat: 检索改为 agent 自主调用工具` | 删预检索/阈值；工具登记编号 + onHits；prompt；chat 编排 |
| 2 | `feat: 引用改为展示映射` | 删 judge；citation 只发可映射的编号；qa_runs 写 answered/failed |
| 3 | `feat: 前端追加多次检索依据` | onRetrieval 追加 + 按 n 去重 |
| 4 | `docs: 记录 ReAct 架构与取舍` | 本文定稿、rag.md/overview/history/AGENTS 同步 |

## 11. 落地记录（2026-11-18）

**后端**

- `chat.Service` 删掉 `searcher` 依赖、预检索、阈值判断、`judge()`、`finishRefused`、`buildPrompt`；
  问题原样交给 agent。
- `agent.Builder` / `Agent` 增加 `onHits` 回调与 `maxResults`（来自 `RAG_TOP_K`）；
  `search_knowledge` 在登记编号后回调，chat 据此发 `retrieval` 事件（可能 0 次或多次）。
- `citationsFromAnswer` 只做映射：解析正文里的 `[n]`，取 registry 里存在的组装 citation；
  找不到的跳过，正文保持纯文本。
- `qa_runs.verdict` 只写 `answered` / `failed`；`retrieved` 存 `registry.All()`。
- `RAG_MIN_SIMILARITY` 从配置与 `.env.example` 删除；`RAG_TOP_K` 变成工具默认条数/上限。
- `agent.Hit` 增加 `RRFScore`，装配层的适配器一并透传，`本次依据` 面板的融合分数不丢。

**前端**

- `onRetrieval` 从覆盖改成"按编号合并 + 排序"：模型多次检索时依据面板不会被冲掉。
- `refused` / `withdrawn` 两种状态保留，但后端不再产生它们。

**验证**

- 后端 `go build ./...`、`go vet ./...`、`go test -race -count=1 ./...`（含真库）全绿。
- 前端 `npm run lint` / `test`（64 用例）/ `build` 全绿。
- 编排单测覆盖：带引用、无引用不撤回、未知编号忽略、零检索、执行失败、客户端断开、builder 启动前失败。

**已知代价**

- 库外问题不再 0 token 拒答：每条问题至少一次模型调用；
- 模型不检索直接答时，系统只展示、不拦截；
- 拒答率指标失效：`qa_runs.verdict` 只剩 answered/failed。
