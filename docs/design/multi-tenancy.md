# MindOS 多租户（用户隔离）技术方案

> 状态：**已落地（2026-11-17）**。第 14 节的决策已确认，第 16 节是实际实现与验收记录。
> 与本文早期方案的两处差异：存量数据直接清空（不再有 `adopt` 与第三段迁移）；
> 注册默认关闭且没有 dev/prod 分支。

## 0. 结论摘要

| 问题 | 结论 |
|---|---|
| 隔离模型 | 一个用户账号就是一个隔离域，业务表统一加 `owner_id bigint not null`（指向 `users.id`） |
| 登录态 | 数据库会话表 + 不透明 token（HttpOnly Cookie），不用 JWT |
| 密码 | bcrypt（cost 12），不做自研哈希、不引入第三方认证服务 |
| 越权语义 | 一律 404（不是 403），避免用状态码探测别人的资源是否存在 |
| 强制手段 | owner 作为**显式参数**穿过 handler → service → repository，靠编译期发现漏传；不靠 GORM 全局钩子 |
| 存量数据 | 三段迁移 + 一次性 `migrate adopt` 命令，不静默把历史数据塞给第一个注册的人 |
| 对象存储 | 原文仍按内容寻址全局共享，删除改为"最后引用者删除"，否则 A 删文档会让 B 的原文消失 |
| 检索 | 向量路加 `d.owner_id` 过滤，并用 pgvector 迭代扫描（实测 0.8.7 支持）补回被过滤掉的候选 |

**明确不做**：组织/团队空间、角色权限矩阵、邮箱验证与找回密码、账号注销、按用户计费与配额、行级安全（RLS）。

## 1. 现状与边界

### 1.1 现在是什么样

- 没有 `users` 表、没有认证中间件；能访问 `:8080` 的人就是使用者。
- 表结构：`documents`、`document_chunks`、`chat_models`、`qa_runs`、`tasks`、`outbox`、`consumed_messages`。
- 唯一约束全部是全局的：`documents.content_hash` 唯一、`chat_models.name` 唯一、`chat_models(is_default)` 部分唯一。
- 原文按 SHA-256 内容寻址存 MinIO（`documents/ab/cd/<sha256>`），**两个用户上传同一份文件会指向同一个对象**，这是本次必须处理的连带问题。
- 检索是一条 SQL 直接扫 `document_chunks JOIN documents`，没有任何"属于谁"的条件。
- 对话模型配置里的 API Key 用全局主密钥 AES-256-GCM 加密，密文与行没有绑定关系。

### 1.2 这次要改什么

1. 注册、登录、登出、改密码；会话可撤销、可过期。
2. 所有业务接口要求登录，所有读写按 `owner_id` 隔离。
3. 检索、问答、向量回填、对话模型配置、问答记录同样隔离。
4. 存量数据有明确的归属迁移路径。
5. 文档与准入约定同步更新（单用户假设作废）。

### 1.3 不改什么

- 部署形态：仍是单进程 + 单 PostgreSQL + MinIO + RabbitMQ。
- 模块边界与分层：仍然是 handler → service → repository。
- 向量模型仍由部署者配置（全局），对话模型仍由使用者配置（本轮起按用户）。
- 任务表与 outbox 的职责划分、`message_id` 语义、reaper 兜底策略都不动。

## 2. 隔离模型

### 2.1 备选方案对比

| 方案 | 做法 | 优点 | 代价 |
|---|---|---|---|
| **A. owner_id（用户即租户）** | 业务表加 `owner_id`，查询带上它 | 改动最小；索引简单；与现有单库单表结构一致 | 将来做团队/协作要再迁一次 |
| B. workspace_id + 成员表 | 用户与空间多对多，隔离键是空间 | 一次到位支持团队 | 现在每张表多一次 JOIN，每个接口多一次成员校验；没有协作需求时是纯负担 |
| C. 每租户一个 schema | `CREATE SCHEMA tenant_1` | 隔离彻底 | 迁移要跑 N 次；连接池要按租户切；GORM 全局表名改写，与"不引入魔法"冲突 |
| D. PostgreSQL RLS | 策略读 `current_setting('app.user_id')` | 数据库层兜底，漏写条件也拦得住 | 每条查询必须跑在设置了 GUC 的事务里；连接池复用会把 GUC 带到下一个请求，漏一次就是全量泄漏；worker 也要特殊处理 |

**选 A。** 理由不是"最省事"，而是当前产品形态下 B 的复杂度没有对应收益，C/D 的失败模式比"应用层漏写一个条件"更难排查（RLS 的 GUC 泄漏是静默的，而且测试很难覆盖"某条 SQL 忘了走事务"）。

D 不是永远不做：RLS 可以以后**叠加**在 A 之上（策略读会话 GUC），那时查询条件已经天然带 owner，加上去只是多一道保险。现在做等于同时改两件事。

### 2.2 隔离键的命名与未来演进

统一叫 `owner_id`，不叫 `tenant_id`：

- 它准确表达"这条数据是谁的"，将来引入 `workspaces` 时，`owner_id` 仍然有意义（创建者/审计），而隔离键会变成 `workspace_id`。
- 现在叫 `tenant_id` 会让人以为系统已经支持组织，而实际上一个租户只有一个用户。

**将来做团队的迁移代价（写在这里是为了让评审看清）**：新建 `workspaces` + `workspace_members`，给每个现有用户建一个个人空间，业务表加 `workspace_id` 并按 `owner_id` 回填，然后把所有查询的隔离键从 `owner_id` 换成 `workspace_id`。因为本轮把所有 owner 条件收口在 repository 层（第 4 节），这次替换是机械的，但要一次数据迁移 + 一次全量回归。**如果确定半年内要做团队协作，现在就该选 B。**

## 3. 身份认证

### 3.1 密码

- **bcrypt，cost 12**（单次约 150–300ms，正好也是暴力破解的成本下限）。用 `golang.org/x/crypto/bcrypt`——它已经在依赖树里（间接），官方文档给的就是 `GenerateFromPassword` / `CompareHashAndPassword` 两个函数，没有需要自己拼的参数编码。
- 不选 Argon2id 的理由：它更强，但要自己编码 salt/参数/版本并处理升级，而本项目的威胁模型里没有"离线爆破 bcrypt-12 划算"这一项。将来要换，做法是在登录成功时按新算法重算哈希（`password_hash` 前缀区分算法），不需要迁移。
- 规则：长度 8–72 **字节**（bcrypt 只取前 72 字节，超长直接拒绝而不是截断——截断会让两个不同的长密码变成同一个），不做大小写/符号的复杂度要求（NIST SP 800-63B 已明确这类规则收益为负）。
- 不存在的邮箱也要执行一次 bcrypt 比较（对一个固定的假哈希），否则"邮箱存在"可以通过响应时间测出来。

### 3.2 会话：数据库 + 不透明 token

为什么不用 JWT：

| | JWT | DB 会话（选定） |
|---|---|---|
| 撤销 | 要维护黑名单，等于回到查库 | 删一行即生效 |
| 密钥 | 要管理签名密钥与轮换 | 无 |
| 每请求成本 | 一次验签 | 一次带索引的主键查找（本项目每条请求本来就要查库） |
| 泄露面 | 存在 localStorage 就被 XSS 拿走 | HttpOnly Cookie，JS 读不到 |

具体形态：

- token = 32 字节 `crypto/rand` → base64url（43 字符），只在响应里出现一次。
- 库里只存 `sha256(token)`。库被 dump 时，攻击者拿到哈希也换不出 token（token 本身是高熵随机值，不需要加盐慢哈希）。
- Cookie：`HttpOnly`、`SameSite=Lax`、`Path=/`、`Secure`（`APP_ENV=prod` 时）、`Max-Age` 与会话 TTL 一致。前端不接触 token，也不写 localStorage。
- TTL：默认 30 天**绝对过期**，不做滑动续期。理由是滑动续期要处理"每次请求都写库"或"续期窗口"，而本地工具 30 天重登一次可以接受。`last_seen_at` 只用于展示，写入做节流（超过 5 分钟才更新一次），避免每个请求一次写。

### 3.3 CSRF

Cookie 认证必须处理 CSRF。三道一起上：

1. `SameSite=Lax`：跨站 POST 不带 Cookie，天然挡住绝大多数场景。
2. **Origin 校验**：非 GET/HEAD 请求若带 `Origin` 头，必须满足其一——host 与请求 `Host` 相同，
   或在 `AUTH_ALLOWED_ORIGINS` 里登记过。浏览器对同源和跨源的 POST 都会带 `Origin`，
   所以这条对浏览器有效；`curl` 不带 `Origin`，放行——这也是 `AGENTS.md` 要求的真实 curl 验证能继续跑的原因。

   **为什么需要白名单（实测缺陷）**：反向代理改写 Host 时（Vite 的 `changeOrigin: true`、
   nginx 默认的 `proxy_set_header Host $proxy_host`），`Origin` 是前端地址、`Host` 是上游地址，
   两者必然不等，注册/登录会被判成跨站。修法两条：开发代理改成 `changeOrigin: false` 保留原始 Host；
   生产或任何改写 Host 的部署，把前端来源登记到 `AUTH_ALLOWED_ORIGINS`。
   不读 `X-Forwarded-Host`——那个头由请求方提供，攻击者可随意伪造，拿它做 CSRF 判据等于没有判据。

3. 拒绝时返回可操作的中文提示（而不是 "cross-site request rejected"），并在日志里打出
   `origin` 与 `host` 两组地址，运维一眼能看出是代理改写还是真的跨站。
4. 所有写操作都是 JSON 或 multipart（不是表单编码），跨站表单提交无法伪造 `Content-Type: application/json` 触发解析（配合前三条已足够）。

不做 double-submit token：它要求前端额外取 token 并回填，收益在 `SameSite=Lax` 面前很小，却给每个写接口增加一个必填头。

### 3.4 注册与暴力破解

- **注册开关**：`AUTH_REGISTRATION=open|closed`，**默认 `closed`**（不按环境分支）。理由很具体：
  向量模型是部署者付费的全局配置，公网开放注册等于让人免费消耗你的 embedding 额度；
  而本项目没有邮箱验证，开放注册也无法阻止垃圾账号。本地开发要注册时临时改成 `open`。
- 注册成功直接建会话并下发 Cookie（不做"注册后再登录"两步）。
- 登录失败：同一账号连续 5 次失败锁定 15 分钟（`users.failed_logins` / `users.locked_until`，成功登录清零）。锁定信息落库而不是放内存，多实例部署时同样有效，重启也不丢。
- IP 维度限流：进程内固定窗口（登录/注册各 10 次/分钟）。单实例够用；多实例时每个实例各自计数，属于"降低噪声"而不是硬防线，这一点写进已知限制。
- 统一错误文案：登录失败只说"邮箱或密码不正确"，不区分"用户不存在/密码错/被禁用"。

### 3.5 表结构

`users` / `sessions` 两张新表：

```sql
CREATE TABLE users (
    id            bigserial   PRIMARY KEY,
    email         text        NOT NULL,
    password_hash text        NOT NULL,
    display_name  text        NOT NULL DEFAULT '',
    status        text        NOT NULL DEFAULT 'active',
    failed_logins integer     NOT NULL DEFAULT 0,
    locked_until  timestamptz,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- 存小写：查询与唯一索引都只认一种写法，避免 Alice@x.com / alice@x.com 变成两个账号。
    CONSTRAINT users_email_check CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    CONSTRAINT users_status_check CHECK (status IN ('active', 'disabled')),
    CONSTRAINT users_failed_logins_check CHECK (failed_logins >= 0)
);
CREATE UNIQUE INDEX users_email_uniq ON users (email);

CREATE TABLE sessions (
    id           bigserial   PRIMARY KEY,
    user_id      bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   bytea       NOT NULL,
    user_agent   text        NOT NULL DEFAULT '',
    client_ip    text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,

    CONSTRAINT sessions_token_hash_check CHECK (length(token_hash) = 32)
);
-- 每个请求都按 token_hash 查一次会话，这条索引是认证路径上唯一的索引。
CREATE UNIQUE INDEX sessions_token_hash_uniq ON sessions (token_hash);
CREATE INDEX sessions_user_idx ON sessions (user_id, created_at DESC);
CREATE INDEX sessions_expire_idx ON sessions (expires_at);
```

会话清理：复用现有 reaper 的周期（每 `REAPER_INTERVAL`）顺手删过期会话，`DELETE FROM sessions WHERE expires_at < now() LIMIT 500`。不引入新的后台协程。

账号不存在删除接口（见 13 节），所以 `users` 的外键全部是默认的 `NO ACTION`：有数据的账号删不掉，这是安全的方向——真要注销，必须连带设计对象存储的清理。

## 4. 请求链路与代码分层

### 4.1 owner 显式传参，不放 context

新增 `internal/auth` 模块（model / password / repository / service / handler / middleware）。

```text
请求 → RequestID → Logger → Recovery → RequireSession → handler → service → repository
                                        │
                                        └─ 从 Cookie 取 token → 查 sessions+users
                                           → c.Set(UserKey) + ctx 里放 Principal（只给日志用）
```

handler 从 gin.Context 取 `Principal`，然后**把 `userID` 作为参数显式传给 service**：

```go
func (h *Handler) Get(c *gin.Context) {
    p, ok := auth.PrincipalFrom(c)      // 中间件保证 ok，防御性判断
    if !ok { httpx.Abort(c, httpx.ErrAuthRequired); return }
    doc, err := h.svc.Get(c.Request.Context(), p.UserID, id)
    ...
}
```

改造后的签名（示意）：

```go
func (s *Service) Get(ctx context.Context, ownerID, id int64) (*Document, error)
func (r *Repository) Get(ctx context.Context, ownerID, id int64) (*Document, error)
func (s *Searcher) Search(ctx context.Context, ownerID int64, query string, limit int, filter DocumentFilter) (*SearchResult, error)
func (s *Service) Ask(ctx context.Context, ownerID int64, in AskInput, emit Emitter) error
func (s *Service) Resolve(ctx context.Context, ownerID int64, id *int64) (*Resolved, error)
```

**为什么不从 context 里取 owner**：context 里的值是"可以没有"的。一旦 repository 写成 `ownerID, _ := auth.FromContext(ctx)`，漏传就变成静默的 `owner_id = 0`（查不到任何数据，看起来像"没有文档"），而不是编译错误。显式参数让**所有调用点在编译期被点名**，这正是这次改动最需要的保障。context 里仍放 Principal，但只给日志与审计读，不参与数据过滤。

### 4.2 唯一的写法约定

repository 里 owner 条件**永远不能是"可选"**：

```go
// 正确：条件写死。ownerID 为 0 时查不到任何行，是安全的失败方向。
db.Where("owner_id = ? AND id = ?", ownerID, id)

// 错误：这就是所有越权事故的源头。
if ownerID > 0 { db = db.Where("owner_id = ?", ownerID) }
```

`DocumentFilter.apply` 是列表现有的唯一收口点，把 `ownerID` 作为构造参数传入：

```go
func (f DocumentFilter) apply(ownerID int64, db *gorm.DB) *gorm.DB {
    db = db.Where("owner_id = ?", ownerID)   // 先加，且不参与任何 if
    ...
}
```

### 4.3 worker / consumer 的信任边界

异步链路里 consumer 只知道 `document_id`，不知道它属于谁。这是合法的：**id 来自我们自己的 outbox 消息，不是来自客户端**。所以保留一组"按 id 直接操作"的内部方法：

| 方法 | 谁调用 | 是否带 owner |
|---|---|---|
| `Get(ctx, ownerID, id)` | HTTP handler | 是 |
| `GetInternal(ctx, id)` | consumer、取消回调 | 否（id 可信） |
| `ProcessImport / ProcessReindex / ProcessEmbed` | consumer | 否（按 id 推进） |
| `MarkFailed / MarkReady / MarkEmbed* / FinishImport` | consumer | 否 |
| `ListEmbedCandidates(ctx, ownerID, model, limit)` | HTTP 回填接口 | 是 |

约束写进注释：`Internal` 方法只允许从 `cmd/server` 的 consumer 与 `internal/worker` 调用，handler 不得调用。评审时专门检查这一点，并在 code review 清单里加一条。

### 4.4 agent 工具链

`agent.Builder` 在启动时创建、`Retriever` 是共享的，所以 owner 不能存在 Builder 上，必须按请求传：

```go
Build(ctx context.Context, ownerID int64, target *chatmodel.Resolved, registry *Registry) (Runner, error)
```

`newSearchTool(retriever, ownerID, ...)` 把 owner 捕获进闭包，工具调用时带上。对应 `agent.Retriever` 接口改成：

```go
Search(ctx context.Context, ownerID int64, query string, limit int) ([]Hit, error)
```

这条链路最容易漏：主检索（宿主预检索）漏了会答别人的文档，工具检索漏了会在多跳问题里"突然看到别人的资料"。两条路径必须有各自的跨租户用例。

## 5. 数据模型改造

### 5.1 逐表改动

| 表 | 改动 | 说明 |
|---|---|---|
| `users` | 新建 | 见 3.5 |
| `sessions` | 新建 | 见 3.5 |
| `documents` | + `owner_id`；`content_hash` 唯一索引 → `(owner_id, content_hash)`；列表索引加 owner 前缀；回填候选索引加 owner | 幂等导入变成"同一用户内幂等"，不同用户上传同一文件各自一条记录 |
| `document_chunks` | **不加** `owner_id` | 只能通过 `document_id` 到达；所有读路径都 JOIN `documents` 并带 owner。加冗余列会让"两处 owner 不一致"成为可能 |
| `qa_runs` | + `owner_id`；索引改 `(owner_id, created_at DESC)`、`(owner_id, verdict, created_at DESC)` | 会话记录（当前唯一的对话落库）按人隔离 |
| `chat_models` | + `owner_id`；`name` 唯一 → `(owner_id, name)`；默认唯一 → `(owner_id) WHERE is_default` | 每人一套模型配置，互不可见、互不可用 |
| `tasks` | 不变 | 内部进度表，无用户可见读接口；`target_id` 全局唯一，不存在跨租户撞键 |
| `outbox` / `consumed_messages` | 不变 | 投递通道与去重，与"谁的数据"无关；payload 里只有 document_id |

`tasks` 不加 `owner_id` 的前提是"任务列表不直接暴露给用户"。现在确实如此（知识页通过文档详情读任务）。**将来如果加"我的任务中心"，再补 owner 列**，而不是现在为假想需求加一个没人读的字段。

### 5.2 子表规则（给后续模块的约定）

> 根表（用户直接拥有的实体）必须有 `owner_id NOT NULL`；子表通过父表约束，任何返回给用户的子表数据都必须 JOIN 父表并带 `owner_id` 条件。

将来的 `conversations`（根，带 owner）→ `messages`（子，只带 conversation_id）、`goals`（根）/ `plans`（子）/ `tasks`（散任务没有父，届时 action 的 tasks 表需要自己的 `owner_id`）。这条规则写进 `AGENTS.md`。

防回归：加一条真库 schema 测试，扫 `information_schema.tables`，除白名单（`users`、`sessions`、`tasks`、`outbox`、`consumed_messages`、`schema_migrations`）外的每张表都必须有 `owner_id`。新模块表忘了加会直接红。

### 5.3 索引与约束的具体改动

```sql
-- documents
ALTER TABLE documents ADD COLUMN owner_id bigint REFERENCES users (id);
DROP INDEX documents_content_hash_key;
CREATE UNIQUE INDEX documents_owner_hash_uniq ON documents (owner_id, content_hash);
DROP INDEX documents_created_at_idx;
CREATE INDEX documents_owner_created_idx ON documents (owner_id, created_at DESC, id DESC);
DROP INDEX documents_embed_pending_idx;
CREATE INDEX documents_embed_pending_idx
    ON documents (owner_id, id) WHERE status = 'ready' AND embed_status <> 'done';

-- qa_runs
ALTER TABLE qa_runs ADD COLUMN owner_id bigint REFERENCES users (id);
DROP INDEX qa_runs_created_at_idx;
DROP INDEX qa_runs_verdict_idx;
CREATE INDEX qa_runs_owner_created_idx ON qa_runs (owner_id, created_at DESC);
CREATE INDEX qa_runs_owner_verdict_idx ON qa_runs (owner_id, verdict, created_at DESC);

-- chat_models
ALTER TABLE chat_models ADD COLUMN owner_id bigint REFERENCES users (id);
DROP INDEX chat_models_name_uniq;
DROP INDEX chat_models_default_uniq;
CREATE UNIQUE INDEX chat_models_owner_name_uniq ON chat_models (owner_id, name);
CREATE UNIQUE INDEX chat_models_owner_default_uniq ON chat_models (owner_id) WHERE is_default;
```

建唯一索引时 `owner_id` 还是 NULL 也没问题：Postgres 认为 NULL 互不相等，存量行不会互相冲突；adopt 回填后再由下一个迁移加 `NOT NULL`（第 7 节）。

### 5.4 原文共享与删除

现在 `Delete` 的顺序是"先删库，提交后删 MinIO 对象"。多用户后同一个 `storage_path` 可能被 N 条文档引用（内容寻址），A 删除时会连带删掉 B 的原文。**这是本次改动里唯一会静默损坏他人数据的地方**，必须一起改：

```text
删除文档（事务：DELETE FROM documents WHERE id=? AND owner_id=?）
  → 提交
  → SELECT EXISTS (SELECT 1 FROM documents WHERE storage_path = ?)
      有 → 什么都不做（别人的文档还在用这份原文）
      无 → 删 MinIO 对象
```

- 检查必须在**提交之后**：并发删同一对象时两个事务都可能看到对方的行，结果是"都不删"（留下孤儿对象）；反过来在事务内先删对象，则可能删掉别人还没提交的引用。孤儿可接受（占空间、不影响正确性），提前删不可接受（别人的文档永久失去原文）。
- 删号/批量清理产生的孤儿对象写入已知限制，留给将来的存储 GC 命令。
- 幂等导入的冲突回退同理：`GetByContentHash` 带 owner，B 上传同内容时不会命中 A 的记录；`SavedFile.Reused` 只表示对象已在存储里。

### 5.5 检索：过滤 + 迭代扫描

向量路加上 `AND d.owner_id = ?` 之后，HNSW 的默认行为是"先按索引取 top N 再过滤"，单用户语料稀疏时会**取不满**：全局 10 万块里某个人的 50 块，问一次只捞回 2 条。正确做法是 pgvector ≥ 0.8 的迭代扫描（本机已实测扩展版本 0.8.7）：

```sql
-- 在一次事务里，只影响本条连接
SET LOCAL hnsw.iterative_scan = 'relaxed_order';
SET LOCAL hnsw.ef_search = 100;      -- 由 RAG_TOP_K 推导，而不是写死
```

- 检索的两条 SQL 要包进同一个事务才能用 `SET LOCAL`；事务很短，只有两条 SELECT。
- 启动时读 `pg_extension.extversion`，< 0.8 时只告警并按"提高候选数（30 → 100）"的退路跑，同时把"召回可能不足"写进已知限制。
- 全文路是普通索引过滤，没有这个问题。

排序、RRF、阈值闸门都不变；`MaxSimilarity` 仍然只来自向量路。

### 5.6 密文绑定 owner（已实现）

给 `api_key_cipher` 加一个 1 字节版本号：

- AAD = `"chat_model:" + owner_id`，AES-GCM 加密/解密都带上。
- 没有 v1 兼容：存量配置在升级时已清空，第一版就是带 AAD 的格式。

收益是具体的：运维手工把 A 的配置行复制给 B、或某个 bug 写错了 `owner_id` 时，
B 会得到"解不开，请重填 Key"，而不是**悄悄用 A 的 Key 花钱**。
集成测试里模拟了"把密文搬到另一个 owner 的行上"，断言解密失败。

## 6. 跨租户不变量与错误语义

### 6.1 越权一律 404

B 访问 A 的文档 / 分块 / 问答记录 / 对话模型：返回 `404 NOT_FOUND`，不是 `403 FORBIDDEN`。403 等于告诉对方"这个 id 存在，只是不给你"，配合自增主键就能枚举出全站数据量与他人资源存在性。

实现上不需要额外代码：所有查询带 owner 条件，查不到就是"不存在"，handler 现有的 `ErrNotFound → 404` 映射自动生效。**不要为越权单独写分支**。

### 6.2 越权测试矩阵（每条都要有真库用例）

| 场景 | 期望 |
|---|---|
| `GET /documents/:id`（别人的） | 404 |
| `PATCH` / `DELETE` / `reindex` / `cancel` / `embed`（别人的） | 404，且文档状态不变 |
| `GET /documents/:id/chunks` | 404 |
| `GET /documents` | 只返回自己的，`total` 也按自己算 |
| `POST /search`、`POST /ask` | 检索不到别人的分块（构造"只有 A 有答案"的问题，断言拒答） |
| `POST /documents/embeddings/backfill` | 只排自己的候选 |
| `PATCH/DELETE /chat-models/:id`、`POST /chat-models/:id/default`、`/test` | 404，且不改动对方的默认项 |
| 提问时传别人的 `chat_model_id` | 404（不能借别人的 Key 花钱） |
| 两人各建同名对话模型 | 都成功（唯一索引带 owner） |
| 两人各设默认模型 | 各自一条默认（部分唯一索引带 owner） |
| 两人上传同一文件 | 两条文档、一份对象；A 删除后 B 仍能重索引 |

### 6.3 不泄漏的东西

- 响应里不带 `owner_id`（客户端不需要，带出去只是多一个可枚举字段）。
- 错误信息不带 SQL、不带别人的标题或 id。
- 日志里可以记 `user_id`，但不记邮箱全文（记 `user_id` 足够定位）。
- `content_hash` 目前会在文档详情里展示。它只能让"已经拥有同一份文件的人"确认重复，不能枚举，保留；如果评审认为这也是信息泄漏，就从响应里去掉（前端只有一处展示，删除成本很低）。

## 7. 迁移与发布

### 7.1 两段迁移，存量业务数据直接清空

评审结论是**清空存量数据**：当前库里的文档、问答与模型配置都是测试数据，没有认领价值。
于是不需要"可空 → 回填 → NOT NULL"的三段式，也不需要 `adopt` 命令。

| 迁移 | 内容 |
|---|---|
| `0012_users_sessions` | 建 `users`、`sessions` |
| `0013_tenant_owner` | 清空 `documents` / `qa_runs` / `chat_models` / `tasks` / `outbox` / `consumed_messages`；
三张表加 `owner_id NOT NULL`；换索引与唯一约束 |

为什么先清空再加列：`owner_id` 是 NOT NULL 且有外键，存量行没有可靠归属；
静默归给某个账号在多租户下就是数据泄漏。清空比猜一个 owner 诚实，
也比"第一个注册的人继承全站数据"安全。

清空**不会**回收 MinIO 里的原文对象（SQL 迁移碰不到对象存储）。它们变成孤儿，
只占空间、不影响正确性；需要的话由运维手动清 bucket。这一点写进已知限制。

### 7.2 发布方式：停机升级

旧代码不会写 `owner_id`，新库的 `NOT NULL` 会让它直接插入失败。所以**迁移与发版必须一起做，
单实例短暂停机**。这在当前部署形态（单进程、单机 compose）下是可接受的；
将来要多实例滚动升级，得改成"先加可空列、双写、再收紧"的三步走，那是另一个量级的工程。

顺序：停服务 → `migrate up`（0012 + 0013）→ 起新版本服务 → 注册账号 → 冒烟。

### 7.3 回滚

- `0013` down：删 `owner_id`、恢复旧索引。**清空的数据不会回来**；
  如果多用户期间产生了内容相同的文档，恢复全局唯一索引会失败，需要先人工去重。
- `0012` down：删两张表（须先回滚 0013，因为 `owner_id` 有外键）。
- 上线前仍然建议 `pg_dump`：迁移本身不可逆。

## 8. API 变更

新增（`/api/v1`，全部不走 SSE，统一信封）：

| 方法 | 路径 | 说明 | 鉴权 |
|---|---|---|---|
| POST | `/auth/register` | 邮箱 + 密码 + 昵称（可选）；成功即登录 | 公开（受开关约束） |
| POST | `/auth/login` | 邮箱 + 密码；成功下发 Cookie | 公开（限流 + 锁定） |
| POST | `/auth/logout` | 删除当前会话并清 Cookie | 需登录 |
| POST | `/auth/logout-all` | 删除该用户全部会话（改密码后也可用） | 需登录 |
| GET | `/auth/me` | 当前用户 `{id,email,display_name,created_at,last_login_at}` | 需登录 |
| PUT | `/auth/password` | 旧密码 + 新密码；成功后吊销其他会话 | 需登录 |

现有接口：**全部移到 `RequireSession` 之后**，路径与响应结构不变（除了越权从"能查到"变成 404）。`/healthz`、`/readyz` 保持公开——它们是探针，不带业务数据，也不该因为登录态而让编排系统以为服务挂了。

`GET /settings`、`GET /system/status` 保留在登录后：它们返回的是部署级信息（依赖健康、迁移版本、向量模型是否配置），本轮不区分管理员。如果以后要区分，"只读部署信息"是第一个该收回管理员角色的接口。

新增错误码（写进 `internal/httpx/errors.go`）：

| code | HTTP | 场景 |
|---|---|---|
| `AUTH_REQUIRED` | 401 | 没有 Cookie / 会话不存在或过期 |
| `AUTH_INVALID_CREDENTIALS` | 401 | 邮箱或密码错（不区分） |
| `AUTH_ACCOUNT_LOCKED` | 429 | 连续失败被临时锁定，带 `Retry-After` |
| `AUTH_ACCOUNT_DISABLED` | 403 | 账号被禁用（会话校验时也会命中） |
| `AUTH_EMAIL_TAKEN` | 409 | 注册邮箱已存在 |
| `AUTH_WEAK_PASSWORD` | 400 | 长度不合规 |
| `AUTH_REGISTRATION_CLOSED` | 403 | 部署关闭了注册 |
| `AUTH_PASSWORD_MISMATCH` | 400 | 改密码时旧密码不对 |
| `AUTH_ORIGIN_MISMATCH` | 403 | Origin 校验失败（CSRF） |

`ErrUnauthorized` 已存在但语义太泛（"unauthorized"），登录失败与"没登录"必须分开：前者前端要提示"账号密码错"，后者要跳登录页。所以上面两个码分开定义，不复用同一个模板。

## 9. 前端改动

| 改动 | 内容 |
|---|---|
| 路由 | `/login`、`/register` 放在 `AppShell` 之外（无导航、无侧栏）；其余路由包一层 `<RequireAuth>` |
| 登录态 | TanStack Query 的 `['auth','me']`；`RequireAuth` 在 pending 时显示骨架，401 时 `<Navigate to="/login?next=...">` |
| API 客户端 | `apiRequest` 增加 `credentials: 'include'`（同源其实是默认，但显式写出来免得以后改基址时踩坑）；401 时触发一次"会话失效"事件 → 清缓存 + 跳登录 |
| SSE | `/ask` 的 fetch 同样带 Cookie；首个响应是 401 JSON 时按普通错误处理并跳登录（现在它只处理业务错误） |
| 侧栏 | 底部显示 `display_name/email` 与"退出登录"；去掉"本地单用户 · Phase 3"这行 |
| 设置页 | 对话模型卡片加一句"仅当前账号可见"；不需要新增页面 |
| 表单 | 纯受控组件，不引入 react-hook-form/zod（现有代码风格就是手写校验） |

不做：记住我、第三方登录、头像上传、邮箱验证页。

## 10. 配置项

`.env.example` 新增（与读取它们的提交同一批落地）：

| 项 | 默认 | 说明 |
|---|---|---|
| `AUTH_SESSION_TTL` | `720h` | 会话绝对有效期（30 天） |
| `AUTH_REGISTRATION` | `closed` | 是否允许自助注册（`open` / `closed`，无环境分支） |
| `AUTH_ALLOWED_ORIGINS` | 空 | 可信前端来源，逗号分隔（如 `http://localhost:5173`）；只在代理改写 Host 时需要 |
| `AUTH_ALLOWED_ORIGINS` | 空 | 可信前端来源，逗号分隔（`http://localhost:5173`）。只在代理改写 Host 时需要 |

## 11. 测试与验收

后端门禁照旧：`go build ./...`、`go vet ./...`、`go test -race -count=1 ./...`。

| 层次 | 覆盖 |
|---|---|
| 单元 | 密码策略与 bcrypt 往返；token 生成/哈希；会话过期与撤销判定；Origin 校验；邮箱归一化 |
| 真库集成 | 同邮箱只能注册一次；锁定/解锁；会话过期后 401 且行被删；禁用用户立即失效；改密码只留当前会话；三张表的唯一索引按 owner 生效 |
| 越权矩阵 | 6.2 的 12 条，全部真库 + 真 handler |
| 检索 | A 的文档对 B 不可见（向量路与全文路各一条）；只有 A 有答案的问题对 B 判 `refused`；`iterative_scan` 生效后 top-k 取满 |
| 对象存储 | 两人同内容 → 一份对象；A 删除后 B 重索引成功；并发删除后对象状态可解释 |
| 会话/CSRF | Cookie 属性（HttpOnly/SameSite/Secure）；跨站 Origin 的 POST → 403；无 Origin 的 curl 正常 |
| 前端 | 登录/注册成功与失败、401 跳转并保留 `next`、退出后回到登录页、两个账号浏览器里互不可见（手工验收） |
| 故障 | 数据库短暂不可用时登录返回 503 而不是 500；reaper 清过期会话；主密钥不匹配时的提示不变 |

## 12. 实施拆分（实际提交）

| # | 提交 | 内容 |
|---|---|---|
| 1 | `feat: 实现注册登录与按用户隔离数据` | 迁移 0012/0013、`internal/auth`、knowledge/chat/chatmodel/agent 的 owner 收口、路由与装配、集成测试；`.env.example` 的两个 `AUTH_*` |
| 2 | `feat: 前端新增登录注册与登录态守卫` | 登录/注册页、`RequireAuth`、401 广播、侧栏账号区、前端用例 |
| 3 | `docs: 记录多租户设计与准入约定` | 本文、AGENTS、overview、rag、README、history |

**为什么后端只有一笔提交**："登录"与"数据隔离"单独拆开都会留下一个不可部署的中间态——
只有登录没有隔离，任何登录用户都能看到全站数据；只有隔离没有登录，就没有 owner 可传。
迁移与代码也必须同一批上线（旧代码不会写 `owner_id`，新库的 NOT NULL 会让它插入失败）。
前端与文档彼此独立，所以分成另外两笔。

## 13. 风险与已知限制

1. **会话表成为每请求热点**：一次索引查找，索引只有 token 哈希；量级（单机、几十用户）下可忽略。
2. **多实例下限流按实例计**：登录锁定靠库（跨实例有效），IP 限速靠内存（跨实例各自计数）。
3. **共享向量模型**：所有用户用部署者配置的 embedding 模型与额度，公网开放注册会被薅。缓解：prod 默认关闭注册。真要对外服务，需要按用户配置或配额，那是独立议题。
4. **HNSW 过滤后的召回**：依赖 `iterative_scan`；扩展低于 0.8 时退化为提高候选数，大语料下仍可能取不满。监控手段是空结果率（`qa_runs.verdict='refused'` 的占比）。
5. **原文孤儿对象**：并发删除或进程崩溃可能留下无人引用的对象，只占空间不影响正确性，清理需要单独的 GC 命令（未做）。
6. **无邮箱验证、无找回密码**：忘记密码只能由部署者直接在库里重置（届时补一个 `migrate reset-password` 命令，本轮不做）。这是自托管工具的合理边界，但要在 README 里写明。
7. **无管理员角色**：任何登录用户都能看 `/system/status` 与 `/settings` 的部署信息。
8. **账号无法注销**：有数据的外键会阻止删除用户，注销流程要连带对象存储清理，未做。
9. **content_hash 全局同值**：见 6.3。
10. **升级必须停机**：见 7.2。

## 14. 评审决策点

按重要性排序，每条都给建议。**2026-11-17 评审结论：1–9 全部按建议通过**；
其中第 3 条改为"默认关闭、不按环境分支"，第 4 条改为"清空存量数据"。

| # | 决策 | 建议 | 影响 |
|---|---|---|---|
| 1 | 隔离域 = 用户（A）还是现在就有 workspace（B） | **A**：半年内不做团队就选 A | 选 B 会让本轮工作量翻倍；选 A 则将来做团队要一次数据迁移 |
| 2 | 会话形态 | **DB 会话 + HttpOnly Cookie** | 换 JWT 会失去"立即撤销"，且要管密钥 |
| 3 | 注册策略 | **默认关闭**，用 `AUTH_REGISTRATION=open` 临时开启 | 若产品要公网自助注册，需要先补邮箱验证或邀请码 |
| 4 | 存量数据归属 | **直接清空**（评审确认都是测试数据） | 省掉 adopt 与第三段迁移；代价是 12 篇文档与 407 块向量不再保留 |
| 5 | 越权语义 | **404** | 改成 403 会让自增 id 变成枚举器 |
| 6 | 会话有效期 | **30 天绝对过期** | 改滑动续期要多一次"续期写库"的节流逻辑 |
| 7 | 密文绑定 owner（AAD v2） | **做** | 不做也能隔离，只是少了"配置行被搬走"这一层 |
| 8 | 是否给 `/settings`、`/system/status` 加管理员角色 | **本轮不加** | 加了就要有角色字段与授予流程，属于权限矩阵 |
| 9 | 前端账号体系是否需要"注销账号" | **不做** | 做的话要设计对象存储 GC 与级联删除顺序 |

## 15. 文档同步清单（落地时一起改）

- `AGENTS.md`：删掉"单用户是硬边界"；第 3 节模块表加 `internal/auth`；第 4 节硬性约束加"租户表必须有 owner_id、owner 条件不可选"；第 8 节新增接口流程加"鉴权与 owner 传递"两步。
- `docs/design/overview.md`：第 1、2 节重写（隔离模型与演进路径），第 5 节每张表加 owner，第 12 节 API 清单标注鉴权，第 15 节非目标里移除"多用户"。
- `docs/design/rag.md`：已知限制第 8 条（"多用户阶段必须补登录与 per-user 配置"）改成已实现；补一句"检索必须带 owner，迭代扫描原因见 multi-tenancy.md 5.5"。
- `README.md`：首段定位、启动步骤（首次注册）、常见故障（忘记密码怎么重置）。
- `backend/.env.example`：两个 `AUTH_*`。
- `docs/history.md`：新增一节记录本次改动、验证数据与踩坑。

以上已于 2026-11-17 全部完成，验收记录见第 16 节。

## 16. 落地验收（2026-11-17）

**迁移**：开发库从版本 11 升到 13。`0013` 清空了旧的单用户业务数据（12 篇文档 / 407 块 /
25 条问答 / 2 条模型配置），`owner_id` 直接以 `NOT NULL` 建出来，没有中间态。

**真库端到端**（`:18080`，独立队列 `mindos.jobs.e2e`，未配置向量模型）：

| # | 操作 | 结果 |
|---|---|---|
| 1 | 注册 A（邮箱大小写混合） | 201，邮箱落库为小写，display_name 取邮箱前缀 |
| 2 | `GET /auth/me`（带 Cookie） | 200 |
| 3 | A 建笔记 | 202 + message_id |
| 4 | A 列表 | `total=1` |
| 5 | 注册 B | 201 |
| 6 | B 列表 | `total=0` |
| 7 | B 读 A 的文档 | **404** |
| 8 | B 改 A 的文档 | **404**，A 的标题未变 |
| 9 | B 删 A 的文档 | **404** |
| 10 | A 仍能读到自己的文档 | 200 |
| 11 | 重复注册同名邮箱（大小写不同） | 409 `AUTH_EMAIL_TAKEN` |
| 12 | 密码错误 | 401 `AUTH_INVALID_CREDENTIALS` |
| 13 | 带跨站 `Origin` 的 POST | 403 `AUTH_ORIGIN_MISMATCH` |
| 14 | 登出后 `/auth/me` | 401 `AUTH_REQUIRED` |
| 15 | A 的文档异步处理完成 | `status=ready`，`chunk_count=1`（新 consumer 正常消费） |
| 16 | A 建对话模型 | 201，首条自动为默认 |
| 17 | B 的模型列表 | 空 |
| 18 | B 把 A 的模型设默认 | **404** `CHAT_MODEL_NOT_FOUND` |
| 19 | B 用 A 的 `chat_model_id` 提问 | **404**（不能借别人的 Key 花钱） |
| 20 | B 建同名模型"默认" | 201（唯一索引带 owner） |
| 21 | A 的模型仍是一条且默认未变 | 200 |

**自动化门禁**：后端 `go build ./...` / `go vet ./...` / `go test -race -count=1 ./...` 全绿
（含新增的 auth 真库用例、knowledge 越权矩阵、chatmodel AAD 搬家用例）；
前端 `npm run lint` / `test`（64 用例）/ `build` 全绿。

**实现中踩到的四个坑**（都已修，留作后来者参考）：

1. **GORM 把 `bytea` 扫进 `[]byte` 会逐元素转换**：`Scan(&tokenHash)` 报
   "converting driver.Value type []uint8 to a uint8"。要扫进一个带 `[]byte` 字段的结构体。
2. **`set_config` 的第二个参数是 text**：直接传 `int` 会被 pgx 以 text 编码拒绝
   （`unable to encode 100 into text format`）。先 `strconv.Itoa`。
3. **`-race` 下 bcrypt cost 12 让 auth 包跑了一分多钟**：测试在 `TestMain` 里把成本降到
   `bcrypt.MinCost`，生产默认值 12 由一条单独的断言守着。
4. **handler 测试拿不到 Principal**：`principalKey` 不导出，测试无法 `c.Set`。
   于是导出 `auth.SetPrincipal`——它也顺带成为将来 API Key 认证的统一写入口。

**与方案的差异**：没有 `adopt` 命令，没有 0014，没有 v1 密文兼容（存量已清空）。
其余按本文执行。

