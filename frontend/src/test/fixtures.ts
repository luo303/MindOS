import type { AuthUser } from '@/api/auth'
import type {
  ChatModel,
  ChunkView,
  DocumentEmbedding,
  DocumentList,
  DocumentView,
  SettingsView,
  SystemStatus,
} from '@/api/types'

/** 已登录用户：路由守卫与侧栏账号区都用它。 */
export const authUserFixture: AuthUser = {
  id: 1,
  email: 'alice@example.com',
  display_name: 'Alice',
  created_at: '2026-11-17T00:00:00Z',
  last_login_at: null,
}

/** 后端成功信封。字段与 httpx.Envelope 保持一致。 */
export function envelope<T>(data: T, requestId = 'test-request-id') {
  return { code: 'OK', message: 'success', data, request_id: requestId }
}

/** 构造一个 JSON 响应；非 2xx 时用 status 指定。 */
export function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

export function systemStatusFixture(overrides: Partial<SystemStatus> = {}): SystemStatus {
  return {
    version: 'test-1',
    postgres: { status: 'up' },
    pgvector: { status: 'up' },
    storage: { status: 'up' },
    migration: { state: 'ok', current_version: 1, latest_version: 1, dirty: false },
    ...overrides,
  }
}

export function settingsFixture(overrides: Partial<SettingsView> = {}): SettingsView {
  return {
    app: { env: 'development', http_addr: ':8080' },
    log: { level: 'debug', format: 'console' },
    db: { host: '127.0.0.1', port: 5432, name: 'mindos', sslmode: 'disable', user: 'mindos' },
    redis: { addr: '127.0.0.1:6379', db: 0, pool_size: 10, password_configured: false },
    storage: {
      endpoint: 'localhost:9000',
      bucket: 'mindos',
      max_upload_bytes: 10485760,
    },
    // 对话模型不在 /settings 里：它由用户配置并存库（/chat-models）。
    embedding: { api_base: '', model: '', api_key_masked: '', api_key_configured: false },
    agent: { max_steps: 12, token_budget: 200000 },
    ...overrides,
  }
}

/** 向量化状态的默认值（已向量化）。单独抽出来是为了让用例只覆盖关心的字段。 */
export function embeddingFixture(overrides: Partial<DocumentEmbedding> = {}): DocumentEmbedding {
  return {
    status: 'done',
    model: 'bge-m3',
    dimensions: 1024,
    chunks: 2,
    embedded_at: '2026-11-16T08:00:00Z',
    ...overrides,
  }
}

export function documentFixture(overrides: Partial<DocumentView> = {}): DocumentView {
  return {
    id: 1,
    title: '集成测试笔记',
    source_type: 'note',
    mime: 'text/markdown',
    size_bytes: 42,
    content_hash: 'a'.repeat(64),
    collection: '',
    tags: [],
    status: 'ready',
    chunk_count: 2,
    created_at: '2026-11-09T07:30:00Z',
    updated_at: '2026-11-09T07:30:00Z',
    ...overrides,
    // embedding 放在 spread 之后重建：Partial<DocumentView> 允许调用方传
    // `embedding: undefined`，直接 spread 会让类型变成可空，编译不过。
    embedding: embeddingFixture(overrides.embedding),
  }
}

export function chunkFixture(overrides: Partial<ChunkView> = {}): ChunkView {
  return {
    id: 1,
    seq: 0,
    content: '# 标题\n\n第一段正文。',
    heading: '标题',
    start_offset: 0,
    end_offset: 12,
    token_count: 12,
    ...overrides,
  }
}

export function documentListFixture(docs: DocumentView[]): DocumentList {
  return { items: docs, total: docs.length, limit: 20, offset: 0 }
}

/** 一条对话模型配置。api_key 只给 hint，与后端"永不返回完整 Key"一致。 */
export function chatModelFixture(overrides: Partial<ChatModel> = {}): ChatModel {
  return {
    id: 1,
    name: '讯飞 Spark',
    base_url: 'https://maas-api.cn-huabei-1.xf-yun.com/v2',
    model: 'spark-x2.6',
    api_key_hint: 'wxyz',
    api_key_configured: true,
    temperature: null,
    max_tokens: null,
    is_default: true,
    last_used_at: null,
    created_at: '2026-11-16T08:00:00Z',
    updated_at: '2026-11-16T08:00:00Z',
    ...overrides,
  }
}
