/**
 * 后端接口的类型镜像。
 *
 * 字段名保持 snake_case：和后端 JSON 完全一致，不做 camelCase 转换。
 * 转换层会引入"哪层用哪种命名"的持续判断成本，而这里的收益只是好看。
 *
 * 这些类型必须与后端 handler 的响应结构同步修改——后端加字段时，
 * 前端这里是唯一需要动的地方（页面通过类型提示自然发现变化）。
 */

export type DependencyState = 'up' | 'down' | 'missing' | 'unknown'

export type DependencyStatus = {
  status: DependencyState
  detail?: string
}

export type MigrationState = 'ok' | 'behind' | 'dirty' | 'not_initialized' | 'unknown'

export type MigrationStatus = {
  state: MigrationState
  current_version: number
  latest_version: number
  dirty: boolean
  detail?: string
}

/** GET /system/status：诊断用，永远 200，健康与否看字段 */
export type SystemStatus = {
  version: string
  postgres: DependencyStatus
  pgvector: DependencyStatus
  /** 对象存储（MinIO）：missing = 能连上但 bucket 不在了 */
  storage: DependencyStatus
  migration: MigrationStatus
}

export type ModelSettings = {
  api_base: string
  model: string
  /** 空串表示未配置；只含前四后四位，永远不是完整密钥 */
  api_key_masked: string
  api_key_configured: boolean
}

/** GET /settings：只读配置视图，密码字段不出现 */
export type SettingsView = {
  app: { env: string; http_addr: string }
  log: { level: string; format: string }
  db: { host: string; port: number; name: string; sslmode: string; user: string }
  redis: { addr: string; db: number; pool_size: number; password_configured: boolean }
  /** 原文只存 MinIO，所以这里没有 driver/dir：位置信息就是端点与 bucket */
  storage: {
    endpoint: string
    bucket: string
    max_upload_bytes: number
  }
  // 对话模型不在 .env 里：它由用户配置并存库，接口在 /chat-models。
  embedding: ModelSettings
  agent: { max_steps: number; token_budget: number; run_timeout_seconds: number }
}

/** 文档状态。Phase 4 同步导入，成功即 ready。 */
export type DocumentStatus = 'pending' | 'parsing' | 'indexing' | 'ready' | 'failed' | 'cancelling'

/**
 * 向量化状态。与文档状态是两件事：
 * 一份文档可以 `ready`（内容可用）但 `failed`（语义检索用不了）。
 */
export type EmbedStatus = 'pending' | 'running' | 'done' | 'failed' | 'skipped'

/** 文档的向量化信息，后端嵌在文档视图里的 embedding 对象 */
export type DocumentEmbedding = {
  status: EmbedStatus
  /** 产生当前向量的模型名；没算过时为空串 */
  model: string
  /** 向量维度；没算过时为 0 */
  dimensions: number
  /** 已经有向量的分块数，done 时应等于 chunk_count */
  chunks: number
  embedded_at: string | null
  error_code?: string
  error_message?: string
}

export type DocumentSourceType = 'file' | 'note' | 'web'

/** GET /documents 与 /documents/:id 共用的文档视图 */
export type DocumentView = {
  id: number
  title: string
  source_type: DocumentSourceType
  mime: string
  size_bytes: number
  content_hash: string
  /** 未分类时是空串（后端把 NULL 收敛成空串，避免前端到处判 null） */
  collection: string
  /** 永远是数组，不是 null——后端已统一成 [] */
  tags: string[]
  status: DocumentStatus
  chunk_count: number
  /** 向量化状态。旧后端可能不带这个键，前端在 api 层统一补默认值 */
  embedding: DocumentEmbedding
  /** 处理最终失败时才有值；成功响应里这两个字段不出现 */
  error_code?: string
  error_message?: string
  created_at: string
  updated_at: string
}

/**
 * 导入接口的响应：多一个 existed。
 *
 * 后端用它区分"本次新建（201）"与"命中既有文档（200）"；
 * 前端据此提示用户"没有重复入库"，而不是一声不响地多出一条看起来一样的记录。
 */
export type ImportedDocumentView = DocumentView & {
  existed: boolean
  /**
   * 本次受理写入 outbox 的消息编号（uuid）。
   * 幂等命中时为空串：没有新建消息，也就没有要等的东西。
   */
  message_id: string
}

/** 列表筛选条件。空串表示不过滤，省掉调用方处理 undefined 的分支。 */
export type DocumentFilters = {
  collection: string
  tag: string
  status: DocumentStatus | ''
}

export type DocumentQuery = DocumentFilters & {
  limit: number
  offset: number
}

/** PATCH /documents/:id 的请求体，只传要改的字段 */
export type DocumentPatch = {
  title?: string
  collection?: string
  tags?: string[]
}

/**
 * 分块。start_offset / end_offset 是 **Unicode 码点**偏移，不是字节、也不是
 * UTF-16 下标——前端要用 Array.from(text) 切片才能对齐，详见页面里的 highlight 说明。
 */
export type ChunkView = {
  id: number
  seq: number
  content: string
  heading?: string
  start_offset: number
  end_offset: number
  token_count: number
}

export type DocumentList = {
  items: DocumentView[]
  total: number
  limit: number
  offset: number
}

export type ChunkList = {
  items: ChunkView[]
  total: number
  limit: number
  offset: number
}

/**
 * 一条对话模型配置（GET /chat-models）。
 *
 * api_key 永远不会出现：后端只返回末四位与"是否已配置"。
 * 前端在编辑态也不能把 hint 回填进密码框——那样一保存就把真 Key 覆盖成四个星号。
 */
export type ChatModel = {
  id: number
  name: string
  base_url: string
  model: string
  /** 末四位；短密钥为空串 */
  api_key_hint: string
  api_key_configured: boolean
  temperature: number | null
  max_tokens: number | null
  is_default: boolean
  last_used_at: string | null
  created_at: string
  updated_at: string
}

/** 新增/修改配置的请求体。api_key 缺省表示"不改"，空串表示"清除"。 */
export type ChatModelInput = {
  name: string
  base_url: string
  model: string
  api_key?: string
  temperature: number | null
  max_tokens: number | null
  is_default?: boolean
}

export type ChatModelProbe = {
  ok: boolean
  model: string
  latency_ms: number
  reply: string
}

/** 裁决结果。failed 表示没有产出结果（上游挂了、超时、用户断开）。 */
export type AskVerdict = 'answered' | 'refused' | 'ungrounded' | 'failed'

/** retrieval 事件里的片段：编号 + 相似度 + 前 120 字。 */
export type AskRetrievalItem = {
  n: number
  document_id: number
  chunk_id: number
  title: string
  seq: number
  similarity: number
  rrf_score: number
  snippet: string
}

/** citation 事件里的条目：带偏移，够前端跳回原文并高亮。 */
export type AskCitationItem = {
  n: number
  document_id: number
  chunk_id: number
  title: string
  seq: number
  start_offset: number
  end_offset: number
  similarity: number
}

export type AskDonePayload = {
  run_id: number
  chat_model_id: number
  prompt_tokens: number
  completion_tokens: number
  latency_ms: number
  awaiting_approval?: boolean
  approval_id?: number
  conversation_id?: number
  user_message_id?: number
}

/**
 * SSE 事件。用可辨识联合而不是 any[]：新增一种事件时，
 * 忘记处理的 switch 分支会让 TS 报错，而不是静默忽略。
 */
export type AskEvent =
  | { type: 'retrieval'; seq: number; payload: { hits: AskRetrievalItem[] } }
  | { type: 'delta'; seq: number; payload: { text: string } }
  | { type: 'verdict'; seq: number; payload: { status: AskVerdict; reason: string } }
  | { type: 'citation'; seq: number; payload: { items: AskCitationItem[] } }
  | {
      type: 'error'
      seq: number
      payload: {
        code: string
        message: string
        retryable: boolean
        detail?: string
        request_id?: string
      }
    }
  | { type: 'done'; seq: number; payload: AskDonePayload }
