import { Loader2, Pencil, RefreshCw, Sparkles, Trash2 } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'

import type { ChunkView, DocumentPatch, DocumentView, EmbedStatus } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/status-pill'

import {
  CHUNK_PAGE_SIZE,
  embedMeta,
  formatBytes,
  formatDate,
  isEmbedInFlight,
  isInFlight,
  sourceLabel,
  splitTags,
  statusMeta,
} from './format'

type DetailProps = {
  doc: DocumentView
  chunks: ChunkView[]
  chunkTotal: number
  chunksLoading: boolean
  /** 编辑态由页面持有：保存成功后页面才需要把它关掉，组件自己关不了。 */
  editing: boolean
  savingMeta: boolean
  reindexing: boolean
  cancelling: boolean
  deleting: boolean
  /** 向量化受理请求是否在飞行中（只影响按钮 loading，不影响状态展示） */
  embedding: boolean
  /**
   * 从问答页跳进来时要高亮的分块序号（0 基）。
   *
   * 用序号而不是 chunk id：URL 里带 seq 更短，而且 seq 是人类看得懂的位置。
   * 为 null 表示不是深链进入，不高亮也不滚动。
   */
  highlightSeq: number | null
  onStartEdit: () => void
  onCancelEdit: () => void
  onSaveMeta: (patch: DocumentPatch) => void
  onReindex: () => void
  onEmbed: () => void
  onCancelProcessing: () => void
  onDelete: () => void
}

/**
 * 文档详情：元信息、编辑表单、危险操作与分块预览。
 *
 * 表单字段的初值来自 doc，页面用 `key={doc.id}` 渲染本组件，
 * 换一条文档时整棵子树重建，草稿自然复位——不需要用 effect 去同步 props。
 */
export function DocumentDetail({
  doc,
  chunks,
  chunkTotal,
  chunksLoading,
  editing,
  savingMeta,
  reindexing,
  cancelling,
  deleting,
  embedding,
  highlightSeq,
  onStartEdit,
  onCancelEdit,
  onSaveMeta,
  onReindex,
  onEmbed,
  onCancelProcessing,
  onDelete,
}: DetailProps) {
  // 深链进入（问答页的引用 chip 跳过来）：等目标分块真的渲染出来再滚动。
  // 依赖 chunks.length 是因为首次渲染时还在加载，元素根本不存在。
  useEffect(() => {
    if (highlightSeq === null) {
      return
    }
    const target = document.getElementById(`chunk-${highlightSeq}`)
    target?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }, [highlightSeq, chunks.length])

  const [confirmingDelete, setConfirmingDelete] = useState(false)
  const embed = doc.embedding
  const embedLabel = embedMeta[embed.status]
  // 忙碌 = 受理请求在飞，或后端已把它标成进行中（受理成功后立刻变 pending）。
  // 两个条件都看，是为了避免"点了按钮、请求还没回来、按钮又变回可点"的闪烁。
  const embedBusy = embedding || isEmbedInFlight(embed.status)

  return (
    <div className="space-y-5">
      <div className="space-y-4">
        <header className="space-y-2">
          <h2 className="text-xl font-semibold tracking-tight break-words">{doc.title}</h2>
          <div className="flex flex-wrap items-center gap-2">
            <StatusPill tone={statusMeta[doc.status].tone}>{statusMeta[doc.status].label}</StatusPill>
            {/* 两条独立的进度线：内容可用（上一枚）不等于可语义检索（这一枚）。 */}
            <StatusPill tone={embedLabel.tone}>{embedLabel.label}</StatusPill>
          </div>
          <p className="text-muted-foreground text-xs">更新于 {formatDate(doc.updated_at)}</p>
          {embed.status === 'failed' && embed.error_message ? (
            <p role="alert" className="text-destructive text-xs break-words">
              向量化失败：{embed.error_message}
              {embed.error_code ? `（${embed.error_code}）` : ''}
              {/* 失败的是"最近一次重建"，不是已有的向量：上次成功的向量仍在，
                  只是可能与当前配置的模型不一致。不写清楚，用户会以为检索彻底坏了。 */}
              {embed.chunks > 0 ? ` 上次成功的 ${embed.chunks} 块向量仍可用。` : ''}
            </p>
          ) : null}
          {/* 失败原因必须就地可见：让用户为了一句"处理失败"去翻后端日志是不合理的。 */}
          {doc.status === 'failed' && doc.error_message ? (
            <p role="alert" className="text-destructive text-xs break-words">
              处理失败：{doc.error_message}
              {doc.error_code ? `（${doc.error_code}）` : ''}
            </p>
          ) : null}
        </header>

        <div className="space-y-3">
          <MetadataRow doc={doc} />

          {editing ? (
            <MetadataForm
              doc={doc}
              saving={savingMeta}
              onCancel={onCancelEdit}
              onSubmit={onSaveMeta}
            />
          ) : (
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="secondary" size="sm" onClick={onStartEdit}>
                <Pencil aria-hidden="true" />
                编辑分类与标签
              </Button>
              <Button variant="ghost" size="sm" onClick={onReindex} disabled={reindexing}>
                {reindexing ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RefreshCw aria-hidden="true" />}
                {reindexing ? '重新切分中' : '重新切分'}
              </Button>
              {/* 向量化与切分是两条独立动作：换模型只该重算向量，不该重切。
                  文档还没 ready 时没有分块可算，直接禁用而不是让用户点出一个 409。 */}
              <Button
                variant="ghost"
                size="sm"
                onClick={onEmbed}
                disabled={embedBusy || doc.status !== 'ready'}
              >
                {embedBusy ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Sparkles aria-hidden="true" />}
                {embedActionLabel(embed.status)}
              </Button>
              {/* 只在后台处理中才给取消入口：其他状态下按了也只会拿到 409。 */}
              {isInFlight(doc.status) ? (
                <Button variant="outline" size="sm" onClick={onCancelProcessing} disabled={cancelling}>
                  {cancelling ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
                  {cancelling ? '取消中' : '取消处理'}
                </Button>
              ) : null}
              <Button variant="ghost" size="sm" onClick={() => setConfirmingDelete(true)} disabled={deleting}>
                <Trash2 aria-hidden="true" />
                删除
              </Button>
            </div>
          )}

          {confirmingDelete ? (
            // 二次确认做成内联区块而不是 window.confirm：
            // 后者在测试环境要 mock，也无法把"会删掉什么"说清楚。
            <div role="alertdialog" aria-label="确认删除文档" className="border-destructive/40 bg-destructive/5 space-y-2 rounded-lg border p-3">
              <p className="text-sm font-medium">确定删除《{doc.title}》？</p>
              <p className="text-muted-foreground text-xs">
                文档、{doc.chunk_count} 个分块与原始文件会一并移除，无法恢复。
              </p>
              <div className="flex items-center gap-2">
                <Button variant="destructive" size="sm" onClick={onDelete} disabled={deleting}>
                  {deleting ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
                  确认删除
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setConfirmingDelete(false)} disabled={deleting}>
                  取消
                </Button>
              </div>
            </div>
          ) : null}
        </div>

        <details className="text-muted-foreground text-xs">
          <summary className="w-fit cursor-pointer py-1.5">文件信息</summary>
          <div className="space-y-1.5 pt-1 break-words">
            <p>{sourceLabel[doc.source_type]} · {doc.mime} · {formatBytes(doc.size_bytes)}</p>
            <p>创建于 {formatDate(doc.created_at)}</p>
            {embed.status === 'done' ? (
              <p>
                {embed.chunks} 块已向量化 · {embed.model} · {embed.dimensions} 维
                {embed.embedded_at ? ` · ${formatDate(embed.embedded_at)}` : ''}
              </p>
            ) : null}
            <p className="font-mono break-all">sha256: {doc.content_hash}</p>
          </div>
        </details>
      </div>

      {chunksLoading ? (
        <div aria-busy="true" className="space-y-3">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        <ChunkList
          chunks={chunks}
          total={chunkTotal}
          maxReturned={CHUNK_PAGE_SIZE}
          highlightSeq={highlightSeq}
        />
      )}
    </div>
  )
}

/**
 * 按钮文案随状态变：done 是"重建"（换模型时用），failed 是"重试"，
 * 其余是"生成"。文案不同是因为用户的目的不同——同一个按钮，
 * 三种意图用同一个词会让人不确定按下去会发生什么。
 */
function embedActionLabel(status: EmbedStatus): string {
  switch (status) {
    case 'done':
      return '重建向量'
    case 'failed':
      return '重试向量化'
    case 'running':
      return '向量化中'
    case 'pending':
      return '已排队'
    default:
      return '生成向量'
  }
}

function MetadataRow({ doc }: { doc: DocumentView }) {  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <span className="text-muted-foreground">分类</span>
      {doc.collection === '' ? (
        <span className="text-muted-foreground">未分类</span>
      ) : (
        <Badge variant="secondary">{doc.collection}</Badge>
      )}
      <span className="text-muted-foreground ml-2">标签</span>
      {doc.tags.length === 0 ? (
        <span className="text-muted-foreground">无</span>
      ) : (
        doc.tags.map((tag) => (
          <Badge key={tag} variant="outline" className="font-normal">
            {tag}
          </Badge>
        ))
      )}
    </div>
  )
}

function MetadataForm({
  doc,
  saving,
  onSubmit,
  onCancel,
}: {
  doc: DocumentView
  saving: boolean
  onSubmit: (patch: DocumentPatch) => void
  onCancel: () => void
}) {
  // 初值来自当前文档；因为父级用 key={doc.id} 渲染，换文档时这里会重新初始化。
  const [title, setTitle] = useState(doc.title)
  const [collection, setCollection] = useState(doc.collection)
  const [tags, setTags] = useState(doc.tags.join(', '))

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    // 只传真正发生变化的字段：PATCH 的语义是"改我传的"，
    // 全量提交会把用户没动过的字段也用当前值覆盖一遍，徒增误改风险。
    const patch: DocumentPatch = {}
    if (title.trim() !== doc.title) {
      patch.title = title.trim()
    }
    if (collection.trim() !== doc.collection) {
      patch.collection = collection.trim()
    }
    const nextTags = splitTags(tags)
    if (nextTags.join(',') !== doc.tags.join(',')) {
      patch.tags = nextTags
    }
    if (Object.keys(patch).length === 0) {
      onCancel()
      return
    }
    onSubmit(patch)
  }

  return (
    <form className="space-y-3" onSubmit={submit}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Input value={title} onChange={(e) => setTitle(e.target.value)} aria-label="文档标题" maxLength={200} />
        <Input
          value={collection}
          onChange={(e) => setCollection(e.target.value)}
          placeholder="分类（留空表示未分类）"
          aria-label="文档分类"
          maxLength={64}
        />
      </div>
      <Input
        value={tags}
        onChange={(e) => setTags(e.target.value)}
        placeholder="标签（逗号分隔，留空表示清空）"
        aria-label="文档标签"
      />
      <div className="flex items-center gap-2">
        <Button type="submit" size="sm" disabled={saving}>
          {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          保存
        </Button>
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
          取消
        </Button>
      </div>
    </form>
  )
}

function ChunkList({
  chunks,
  total,
  maxReturned,
  highlightSeq,
}: {
  chunks: ChunkView[]
  total: number
  maxReturned: number
  highlightSeq: number | null
}) {
  return (
    <section className="space-y-3 border-t pt-4" aria-labelledby="chunks-heading">
      <h3 id="chunks-heading" className="text-sm font-semibold">分块预览</h3>

      {chunks.length < total ? (
        <p className="text-muted-foreground text-xs">
          共 {total} 块，这里只显示前 {maxReturned} 块。
        </p>
      ) : null}

      {total === 0 ? <p className="text-muted-foreground text-sm">暂无分块</p> : null}

      {chunks.map((chunk) => (
        <article
          key={chunk.id}
          id={`chunk-${chunk.seq}`}
          className={highlightSeq === chunk.seq ? 'bg-accent/50 ring-primary/40 rounded-md p-3 ring-2' : 'rounded-md p-3'}
        >
          <header className="mb-2 space-y-1">
            <h4 className="text-sm font-medium break-words">
              <span className="text-muted-foreground mr-2 font-mono">#{chunk.seq}</span>
              {chunk.heading}
            </h4>
            <p className="text-muted-foreground font-mono text-xs" title="偏移以 Unicode 码点计">
              [{chunk.start_offset}, {chunk.end_offset}) · 约 {chunk.token_count} tokens
            </p>
          </header>
          {/* 保留原始换行与 Markdown 标记：分块内容就是原文子串。 */}
          <pre className="text-sm leading-7 wrap-anywhere whitespace-pre-wrap [font-family:inherit]">
            {chunk.content}
          </pre>
        </article>
      ))}
    </section>
  )
}
