# 长期记忆 v2（实现设计）

用户确认默认自动记忆。新消息不按前缀过滤，后台一次调用最多输出三项有依据的记忆变更；已有固定偏好迁移时保留其 ID、来源、版本及候选状态。自动保存只用于模型认定明确且通过宿主边界检查的新事实，无依据的建议丢弃，冲突、推断和修改已保存信息时等待确认。记忆是数据，不是授予 Agent 权限的指令。

实现边界：

- `0025` 扩展原三张表，增加多来源证据、消费回执和账号/会话记忆模式。按账号隔离，从消息到候选再到版本在事务内核对来源及目标。
- `internal/memory` 负责结构化提炼、去重、修订、有效期和有限上下文；`chat` 只提供来源和调用服务。提炼失败不阻塞 SSE；Worker 不持有数据库锁调用模型。
- 记忆正文是可编辑的短文本；类别用于整理。已保存项按相关性召回，明确置顶的全局规则优先，本轮用户要求优先。待确认、归档、到期和其它账号的数据不参与召回；删除已保存条目的来源消息后仍保留结构化正文。
- 记忆页提供画像/全部记忆/待确认、手动增改、版本与来源、自动/仅建议模式。聊天由同一候选组件提供宽屏侧栏和窄屏抽屉，独立轮询，不依赖回答流。

## 提炼与召回边界

- 入队时只写 ID；消费者读取当前原文、最多四条邻近消息和最多十二条相关记忆。JSON 输出最多三项，正文最多 150 字，要求当前原文中的连续摘录；规则校验类别/长度/有效期、目标 ID 与基础版本、常见凭据字样。模型判断与字面校验不是绝对准确的敏感信息分类器。
- 模型调用超时 20 秒、输出上限 1200 token，单进程一次提炼一个任务，失败由现有 dispatch 重试。账号设置行锁协调消费者和手动写入，消费回执避免已经处理的消息重复写入。关闭模式不会删除既有记忆，已经完成的写入不会撤销。
- 当前召回基线在 PostgreSQL 读取最近最多 300 条有效记忆（置顶优先），按英文单词与中文相邻字重合评分，给全局偏好和规则基础权重；最多八条进入回答。范围名称须在问题中出现。当前未建 ES 记忆索引、向量召回或全局语义去重，无法保证识别所有同义/冲突陈述；后续需结合真实提炼样本衡量准确性。历史消息不回填。

## 数据与接口

`0025_open_memory` 扩展 `memories`、`memory_versions` 和 `memory_candidates`，增加 `memory_evidence`、`memory_extractions`、`memory_settings`、`memory_conversation_settings`。旧偏好兼容原接口。先在 `backend/` 运行 `go run ./cmd/migrate up`，再重启前后端。回滚遇到 v1 无法表示的开放数据会拒绝执行。

| 接口 | 说明 |
|---|---|
| `GET /memories` | 当前条目，含归档项 |
| `POST /memories` | `value`、`category`、`scope`、`expires_at`、`pinned` |
| `PATCH /memories/:id` | `expected_version`、可选 `value`、`status`、`metadata` |
| `GET /memories/:id/versions` | 版本快照，支持 `before_version` |
| `GET /memories/:id/evidence` | 来源摘录及会话消息 ID，最多 100 项 |
| `GET /memory-candidates` | 待确认列表，可按 `conversation_id` 过滤，最多 50 项 |
| `POST /memory-candidates/:id/decision` | `decision: accept/reject`，可附 `value` |
| `GET /memory-settings` | 账号模式，可查 `conversation_id` 取得会话覆写 |
| `PATCH /memory-settings` | `mode: auto/suggest/off` 或 `conversation_id`、`conversation_off` |

验收覆盖普通消息入队、凭据拒绝、自动保存和仅建议、同一消息多项、重复消费、追加证据、冲突版本、过期过滤、账号隔离、关闭会话记忆。真实 RabbitMQ 和模型的完整效果仍须在实际运行环境观察。
