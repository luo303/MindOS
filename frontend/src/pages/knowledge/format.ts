import type { DocumentSourceType, DocumentStatus, EmbedStatus } from '@/api/types'

/** 列表每页条数，与后端的默认值一致；改这里时后端默认值不用动，因为请求里显式带了 limit。 */
export const PAGE_SIZE = 20

/** 分块接口一次最多返回 100 条（后端上限），页面据此提示"未全部显示"。 */
export const CHUNK_PAGE_SIZE = 100

export const sourceLabel: Record<DocumentSourceType, string> = {
  file: '文件',
  note: '笔记',
  web: '网页',
}

type Tone = 'ok' | 'warn' | 'danger' | 'muted'

export const statusMeta: Record<DocumentStatus, { label: string; tone: Tone }> = {
  pending: { label: '待处理', tone: 'muted' },
  parsing: { label: '解析中', tone: 'warn' },
  indexing: { label: '索引中', tone: 'warn' },
  ready: { label: '已解析', tone: 'ok' },
  failed: { label: '失败', tone: 'danger' },
  cancelling: { label: '取消中', tone: 'muted' },
}

/** 状态下拉项。第一项是"不过滤"，所以值是空串而不是 undefined。 */
export const statusOptions: { value: DocumentStatus | ''; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'ready', label: statusMeta.ready.label },
  { value: 'pending', label: statusMeta.pending.label },
  { value: 'parsing', label: statusMeta.parsing.label },
  { value: 'indexing', label: statusMeta.indexing.label },
  { value: 'failed', label: statusMeta.failed.label },
  { value: 'cancelling', label: statusMeta.cancelling.label },
]

/**
 * 是否还在后台处理中（Phase 6 起导入是异步的）。
 *
 * 这三个状态意味着"现在看到的不是最终结果"，页面对它们要轮询；
 * ready / failed / cancelling 都不再轮询，免得请求永远停不下来。
 */
const IN_FLIGHT: DocumentStatus[] = ['pending', 'parsing', 'indexing']

export function isInFlight(status: DocumentStatus): boolean {
  return IN_FLIGHT.includes(status)
}

/** 轮询间隔：1s 能让"处理中 → 已解析"的过渡看得见，又不至于把接口打爆。 */
export const POLL_INTERVAL_MS = 1000

/**
 * 向量化状态的展示文案。
 *
 * skipped 的文案刻意写成"未配置向量模型"而不是"未向量化"：
 * 前者告诉用户下一步该做什么（去 Settings 填 Key），后者只说明现状。
 */
export const embedMeta: Record<EmbedStatus, { label: string; tone: Tone }> = {
  pending: { label: '待向量化', tone: 'muted' },
  running: { label: '向量化中', tone: 'warn' },
  done: { label: '已向量化', tone: 'ok' },
  failed: { label: '向量化失败', tone: 'danger' },
  skipped: { label: '未配置向量模型', tone: 'muted' },
}

/**
 * 向量化是否还在进行。
 *
 * 必须和 isInFlight 一起用于轮询判断：向量化发生在文档 ready *之后*，
 * 只看 documents.status 的话，用户永远看不到"向量化中 → 已完成"。
 */
export function isEmbedInFlight(status: EmbedStatus): boolean {
  return status === 'pending' || status === 'running'
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

export function formatDate(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return iso
  }
  return date.toLocaleString('zh-CN', { hour12: false })
}

/**
 * 把"逗号分隔的输入框内容"拆成标签数组。
 *
 * 去空白、丢空项、按原顺序去重：与服务端 normalizeTags 的规则保持一致。
 * 前端先做一遍不是重复劳动——用户能立刻看到自己输入的会被切成几个标签，
 * 而不用等一次往返才知道。
 */
export function splitTags(raw: string): string[] {
  const seen = new Set<string>()
  const tags: string[] = []
  for (const piece of raw.split(',')) {
    const tag = piece.trim()
    if (tag === '' || seen.has(tag)) {
      continue
    }
    seen.add(tag)
    tags.push(tag)
  }
  return tags
}
