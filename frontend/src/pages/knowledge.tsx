import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, NotebookPen, SlidersHorizontal, Sparkles, Upload, X } from 'lucide-react'
import { useEffect, useRef, useState, type ChangeEvent } from 'react'
import { useSearchParams } from 'react-router'

import {
  backfillEmbeddings,
  cancelProcessing,
  createNote,
  deleteDocument,
  documentKeys,
  embedDocument,
  fetchChunks,
  fetchDocument,
  fetchDocuments,
  reindexDocument,
  updateDocument,
  uploadDocument,
} from '@/api/documents'

import { fetchSettings, queryKeys } from '@/api/system'
import { describeError } from '@/api/errors'
import type { DocumentFilters, DocumentPatch, DocumentQuery, ImportedDocumentView } from '@/api/types'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

import { DocumentDetail } from '@/pages/knowledge/document-detail'
import { DocumentList } from '@/pages/knowledge/document-list'
import { FilterBar } from '@/pages/knowledge/filter-bar'
import { isEmbedInFlight, isInFlight, PAGE_SIZE, POLL_INTERVAL_MS } from '@/pages/knowledge/format'
import { NoteComposer, type NoteDraft } from '@/pages/knowledge/note-composer'
import { UploadComposer, type UploadDraft } from '@/pages/knowledge/upload-composer'

const emptyFilters: DocumentFilters = { collection: '', tag: '', status: '' }

/**
 * 知识库页负责取数和变更，列表、详情和导入表单独立渲染。
 */
export function KnowledgePage() {
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  // 问答页的引用 chip 会带 ?doc=&chunk= 跳进来。
  // chunk 是 0 基的分块序号，只用于滚动与高亮；doc 决定打开哪篇文档。
  const [searchParams] = useSearchParams()
  const deepLinkDoc = Number(searchParams.get('doc'))
  const hasDeepLink = Number.isFinite(deepLinkDoc) && deepLinkDoc > 0
  const chunkParam = searchParams.get('chunk')
  const deepLinkChunk = chunkParam === null ? null : Number(chunkParam)

  const [page, setPage] = useState(1)
  const [filters, setFilters] = useState<DocumentFilters>(emptyFilters)
  const [selectedId, setSelectedId] = useState<number | null>(hasDeepLink ? deepLinkDoc : null)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [composerOpen, setComposerOpen] = useState(false)
  const [uploadOpen, setUploadOpen] = useState(false)
  const [pendingFile, setPendingFile] = useState<File | null>(null)
  const [importNotice, setImportNotice] = useState<{ existed: boolean; title: string } | null>(null)
  const [backfillNotice, setBackfillNotice] = useState<string | null>(null)
  const [filterOpen, setFilterOpen] = useState(false)

  // URL 里的 doc 变化时切换选中（例如在页面内又点了一次别的引用）。
  // 用 ref 记住上次处理过的值：否则用户点列表换文档后，这个 effect 会因为
  // selectedId 变了而再把它改回 URL 里那篇，形成"列表点不动"的假象。
  const lastDeepLinkRef = useRef<number | null>(null)
  useEffect(() => {
    if (!hasDeepLink || lastDeepLinkRef.current === deepLinkDoc) {
      return
    }
    lastDeepLinkRef.current = deepLinkDoc
    setSelectedId(deepLinkDoc)
  }, [hasDeepLink, deepLinkDoc])

  const offset = (page - 1) * PAGE_SIZE
  const query: DocumentQuery = { limit: PAGE_SIZE, offset, ...filters }
  const listQuery = useQuery({
    queryKey: documentKeys.list(query),
    queryFn: ({ signal }) => fetchDocuments(query, signal),
    // 导入是异步的：只要列表里还有文档在处理中，就每秒重取一次，
    // 让"待处理 → 已解析"自然过渡。全部终态后自动停，不会一直空转。
    refetchInterval: (q) =>
      q.state.data?.items.some(
        (item) => isInFlight(item.status) || isEmbedInFlight(item.embedding.status),
      )
        ? POLL_INTERVAL_MS
        : false,
  })

  const items = listQuery.data?.items ?? []
  const total = listQuery.data?.total ?? 0
  // 不选时默认看最新一条：新增后列表会刷新，用户不需要再点一下。
  const activeId = selectedId ?? items[0]?.id ?? null

  const detailQuery = useQuery({
    queryKey: documentKeys.detail(activeId ?? 0),
    queryFn: ({ signal }) => fetchDocument(activeId as number, signal),
    enabled: activeId !== null,
    // 详情与列表各自轮询：详情是"当前这一篇"，列表是"有没有别的还在跑"，
    // 只看其中一个都会出现"列表变了但详情还显示处理中"。
    // 两个条件都要看：向量化发生在文档 ready 之后，只看 status 的话
    // 用户永远等不到"向量化中 → 已完成"那一次刷新。
    refetchInterval: (q) =>
      q.state.data &&
      (isInFlight(q.state.data.status) || isEmbedInFlight(q.state.data.embedding.status))
        ? POLL_INTERVAL_MS
        : false,
  })
  const chunksQuery = useQuery({
    queryKey: documentKeys.chunks(activeId ?? 0),
    queryFn: ({ signal }) => fetchChunks(activeId as number, signal),
    enabled: activeId !== null,
    // 分块也必须跟着轮询，否则会出现"状态已经变成已解析、分块还是空"的割裂：
    // 详情与分块是两个请求，只让详情轮询的话，用户的观感就是"说会自动刷新，
    // 其实得切走再切回"。两个条件任一成立就继续跟：
    //   1. 文档还在处理中；
    //   2. 处理完了但分块数和文档记的对不上（差那一拍，再取一次就齐了）。
    refetchInterval: (q) => {
      const doc = detailQuery.data
      if (!doc) {
        return false
      }
      if (isInFlight(doc.status)) {
        return POLL_INTERVAL_MS
      }
      const loaded = q.state.data?.total ?? 0
      return loaded === doc.chunk_count ? false : POLL_INTERVAL_MS
    },
  })

  // 只读一次配置：用它决定要不要显示"补齐向量"入口与顶部提示。
  // 复用 Settings 页的 key，两个页面共享同一份缓存，不会各查一遍。
  const settingsQuery = useQuery({
    queryKey: queryKeys.settings,
    queryFn: ({ signal }) => fetchSettings(signal),
    staleTime: 60_000,
  })
  // 用可选链读：旧后端的 /settings 里还没有 embedding 键，
  // 缺了就当"没配置"处理，页面照常可用。
  const embeddingConfigured = settingsQuery.data?.embedding?.api_key_configured ?? false

  const invalidateDocuments = () => {
    // 失效整个 documents 前缀：列表、详情、分块一起重取，
    // 比手工挑几个 key 更不容易漏。
    void queryClient.invalidateQueries({ queryKey: documentKeys.all })
  }

  const afterImport = (doc: ImportedDocumentView) => {
    setSelectedId(doc.id)
    setPage(1)
    setComposerOpen(false)
    setUploadOpen(false)
    setPendingFile(null)
    // 幂等命中时说清楚"没有新建"，否则用户会以为又存了一份。
    setImportNotice({ existed: doc.existed, title: doc.title })
    invalidateDocuments()
  }

  const uploadMutation = useMutation({
    mutationFn: (draft: UploadDraft & { file: File }) =>
      // 空值不传：后端把"没有这个字段"与"传了空串"都当未设置，
      // 但少一个字段能让请求体更接近用户真实意图（与笔记导入一致）。
      uploadDocument(draft.file, { collection: draft.collection, tags: draft.tags }),
    onSuccess: afterImport,
  })
  const noteMutation = useMutation({
    mutationFn: (draft: NoteDraft) =>
      createNote({ content: draft.content, title: draft.title, collection: draft.collection, tags: draft.tags }),
    onSuccess: afterImport,
  })
  const updateMutation = useMutation({
    mutationFn: (patch: DocumentPatch) => updateDocument(activeId as number, patch),
    onSuccess: () => {
      setEditingId(null)
      invalidateDocuments()
    },
  })
  const deleteMutation = useMutation({
    mutationFn: () => deleteDocument(activeId as number),
    onSuccess: () => {
      // 删掉的是当前选中的那一条，选中态必须清掉：
      // 留着它会立刻触发一次注定 404 的详情请求。
      setSelectedId(null)
      setEditingId(null)
      setImportNotice(null)
      invalidateDocuments()
    },
  })
  const reindexMutation = useMutation({
    mutationFn: () => reindexDocument(activeId as number),
    onSuccess: invalidateDocuments,
  })
  const cancelMutation = useMutation({
    mutationFn: () => cancelProcessing(activeId as number),
    // 取消成功后不额外改本地状态：文档状态由后端写，轮询会把它带到终态，
    // 本地再猜一次只会出现"页面说已取消、库里还是 cancelling"的短暂矛盾。
    onSuccess: invalidateDocuments,
  })
  // 重建向量：只重算向量，不重切分块。受理后文档立刻变"待向量化"，
  // 之后的进展靠轮询带回来，这里不猜状态。
  const embedMutation = useMutation({
    mutationFn: () => embedDocument(activeId as number),
    onSuccess: () => {
      setBackfillNotice(null)
      invalidateDocuments()
    },
  })
  // 批量补齐：配置好模型后，历史上那些 skipped / 旧模型算过的文档需要一个入口。
  // 返回 remaining，让人知道要不要再点一次。
  const backfillMutation = useMutation({
    mutationFn: () => backfillEmbeddings(),
    onSuccess: (res) => {
      setBackfillNotice(
        `已排队 ${res.enqueued} 篇，跳过 ${res.skipped} 篇（已在排队）` +
          (res.remaining > 0 ? `，还剩 ${res.remaining} 篇待补齐，可再点一次。` : '，没有剩余待补齐的文档。'),
      )
      invalidateDocuments()
    },
  })

  const onFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    // 清空 value：否则再选同一个文件不会触发 change，用户会以为点击没生效。
    event.target.value = ''
    if (file) {
      // 选中文件不等于要导入：先摆进确认面板，让人有机会填分类与标签。
      setPendingFile(file)
      setUploadOpen(true)
      setComposerOpen(false)
    }
  }

  /** 打开上传面板并立刻弹文件框：两次点击合成一次，选完文件才轮到填元信息。 */
  const startUpload = () => {
    setUploadOpen(true)
    setComposerOpen(false)
    fileInputRef.current?.click()
  }

  const changeFilters = (next: DocumentFilters) => {
    setFilters(next)
    setPage(1)
    setSelectedId(null)
    setFilterOpen(false)
  }

  const selectDocument = (id: number) => {
    setSelectedId(id)
  }

  const actionError =
    updateMutation.error ??
    deleteMutation.error ??
    reindexMutation.error ??
    cancelMutation.error ??
    embedMutation.error ??
    backfillMutation.error
  // 有筛选条件时的空结果与"一条文档都没有"是两种状态，文案要分开。
  const isFiltered = filters.collection !== '' || filters.tag !== '' || filters.status !== ''

  const filterCount = [filters.collection, filters.tag, filters.status].filter(Boolean).length

  return (
    <div className="flex h-full min-h-0 flex-col md:flex-row">
      <aside className="bg-muted/20 flex max-h-[45%] w-full shrink-0 flex-col border-b md:max-h-none md:w-72 md:border-r md:border-b-0">
        <div className="space-y-2 border-b p-3">
          <div className="flex items-baseline justify-between">
            <h1 className="text-lg font-semibold tracking-tight">知识库</h1>
            <span className="text-muted-foreground text-xs tabular-nums">{total}</span>
          </div>

          <input
            ref={fileInputRef}
            type="file"
            accept=".md,.markdown,.txt,.text"
            className="hidden"
            onChange={onFileChange}
            aria-label="选择要导入的文件"
          />

          <div className="grid grid-cols-2 gap-2">
            <Button
              size="sm"
              onClick={() => {
                setComposerOpen((open) => !open)
                setUploadOpen(false)
              }}
              aria-expanded={composerOpen}
              className="justify-start"
            >
              <NotebookPen aria-hidden="true" />
              写笔记
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={startUpload}
              disabled={uploadMutation.isPending}
              className="justify-start"
            >
              {uploadMutation.isPending ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : (
                <Upload aria-hidden="true" />
              )}
              {uploadMutation.isPending ? '导入中' : '上传文件'}
            </Button>
            {embeddingConfigured ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => backfillMutation.mutate()}
                disabled={backfillMutation.isPending}
                className="justify-start"
              >
                {backfillMutation.isPending ? (
                  <Loader2 className="animate-spin" aria-hidden="true" />
                ) : (
                  <Sparkles aria-hidden="true" />
                )}
                补齐向量
              </Button>
            ) : null}
            <Button
              variant="outline"
              size="sm"
              onClick={() => setFilterOpen(true)}
              className="justify-start"
            >
              <SlidersHorizontal aria-hidden="true" />
              筛选
              {filterCount > 0 ? (
                <span className="bg-primary text-primary-foreground ml-auto rounded-full px-1.5 text-[10px] tabular-nums">
                  {filterCount}
                </span>
              ) : null}
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-2">
          {uploadMutation.error ? (
            <InlineError title="文件导入失败" description={describeError(uploadMutation.error)} />
          ) : null}
          {noteMutation.error ? (
            <InlineError title="笔记保存失败" description={describeError(noteMutation.error)} />
          ) : null}
          {actionError ? <InlineError title="操作失败" description={describeError(actionError)} /> : null}
          {importNotice ? <ImportNotice notice={importNotice} onDismiss={() => setImportNotice(null)} /> : null}
          {backfillNotice ? (
            <div role="status" className="border-border bg-muted/50 mb-2 flex items-start justify-between gap-3 rounded-lg border px-3 py-2">
              <p className="text-sm">{backfillNotice}</p>
              <Button variant="ghost" size="sm" onClick={() => setBackfillNotice(null)} aria-label="关闭提示">
                <X aria-hidden="true" />
              </Button>
            </div>
          ) : null}

          <section aria-label="文档列表">
            {listQuery.isPending ? (
              <ListSkeleton />
            ) : listQuery.error ? (
              <ErrorState
                title="读取文档列表失败"
                description={describeError(listQuery.error)}
                onRetry={() => void listQuery.refetch()}
                retrying={listQuery.isFetching}
              />
            ) : items.length === 0 ? (
              <EmptyState title={isFiltered ? '没有符合条件的文档' : '还没有文档'} />
            ) : (
              <DocumentList
                items={items}
                total={total}
                page={page}
                activeId={activeId}
                onSelect={selectDocument}
                onPageChange={setPage}
              />
            )}
          </section>
        </div>
      </aside>

      <main className="min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl p-4 sm:p-6">
          {composerOpen ? (
            <NoteComposer
              saving={noteMutation.isPending}
              onCancel={() => setComposerOpen(false)}
              onSubmit={(draft) => noteMutation.mutate(draft)}
            />
          ) : null}

          {uploadOpen ? (
            <UploadComposer
              file={pendingFile}
              saving={uploadMutation.isPending}
              onChooseFile={() => fileInputRef.current?.click()}
              onCancel={() => {
                setUploadOpen(false)
                setPendingFile(null)
              }}
              onSubmit={(draft) => {
                if (pendingFile) {
                  uploadMutation.mutate({ ...draft, file: pendingFile })
                }
              }}
            />
          ) : null}

          {activeId !== null ? (
            <section aria-label="文档详情">
              {detailQuery.isPending ? (
                <ListSkeleton />
              ) : detailQuery.error ? (
                <ErrorState
                  title="读取文档详情失败"
                  description={describeError(detailQuery.error)}
                  onRetry={() => void detailQuery.refetch()}
                  retrying={detailQuery.isFetching}
                />
              ) : (
                <DocumentDetail
                  key={detailQuery.data.id}
                  doc={detailQuery.data}
                  chunks={chunksQuery.data?.items ?? []}
                  chunkTotal={chunksQuery.data?.total ?? 0}
                  chunksLoading={chunksQuery.isPending}
                  highlightSeq={activeId === deepLinkDoc ? deepLinkChunk : null}
                  savingMeta={updateMutation.isPending}
                  reindexing={reindexMutation.isPending}
                  embedding={embedMutation.isPending}
                  cancelling={cancelMutation.isPending}
                  deleting={deleteMutation.isPending}
                  editing={editingId === detailQuery.data.id}
                  onStartEdit={() => setEditingId(detailQuery.data.id)}
                  onCancelEdit={() => setEditingId(null)}
                  onSaveMeta={(patch) => updateMutation.mutate(patch)}
                  onReindex={() => reindexMutation.mutate()}
                  onEmbed={() => embedMutation.mutate()}
                  onCancelProcessing={() => cancelMutation.mutate()}
                  onDelete={() => deleteMutation.mutate()}
                />
              )}
            </section>
          ) : null}
        </div>
      </main>

      {filterOpen ? (
        <div
          className="fixed inset-0 z-[80] flex items-start justify-center bg-black/35 p-4 pt-[12vh]"
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) {
              setFilterOpen(false)
            }
          }}
        >
          <div role="dialog" aria-modal="true" aria-label="筛选文档" className="bg-background w-full max-w-xl rounded-2xl border p-4 shadow-2xl">
            <div className="mb-3 flex items-center justify-between gap-3">
              <h2 className="text-base font-semibold">筛选</h2>
              <Button variant="ghost" size="icon" aria-label="关闭筛选" onClick={() => setFilterOpen(false)}>
                <X aria-hidden="true" />
              </Button>
            </div>
            <FilterBar filters={filters} onChange={changeFilters} />
          </div>
        </div>
      ) : null}
    </div>
  )
}

function ImportNotice({
  notice,
  onDismiss,
}: {
  notice: { existed: boolean; title: string }
  onDismiss: () => void
}) {
  return (
    // role=status：这是"操作完成"的提示，不是错误，用 status 让读屏器温和播报。
    <div role="status" className="border-border bg-muted/50 flex items-start justify-between gap-3 rounded-lg border px-3 py-2">
      <p className="text-sm">
        {notice.existed
          ? `内容与已有文档一致，已复用《${notice.title}》，没有重复入库。`
          : `已受理《${notice.title}》，正在后台解析，完成后会自动刷新。`}
      </p>
      <Button variant="ghost" size="sm" onClick={onDismiss} aria-label="关闭提示">
        <X aria-hidden="true" />
      </Button>
    </div>
  )
}

function InlineError({ title, description }: { title: string; description: string }) {
  return (
    // role=alert：失败是异步出现的，屏幕阅读器需要主动播报。
    <div role="alert" className="border-destructive/40 bg-destructive/5 rounded-lg border px-3 py-2">
      <p className="text-sm font-medium">{title}</p>
      <p className="text-muted-foreground text-xs break-words">{description}</p>
    </div>
  )
}

function ListSkeleton() {
  return (
    <div aria-busy="true" className="space-y-3">
      <Skeleton className="h-20 w-full" />
      <Skeleton className="h-20 w-full" />
    </div>
  )
}
