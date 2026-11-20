import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, Plus, SendHorizontal, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'

import { approvalKeys, fetchConversationApprovals, streamApprovalDecision } from '@/api/approvals'
import { chatModelKeys, fetchChatModels } from '@/api/chat-models'
import {
  conversationKeys,
  createConversation,
  deleteConversation,
  fetchConversations,
  fetchMessages,
  sendConversationMessage,
  type ChatMessage,
} from '@/api/conversations'
import { ApiError, describeError } from '@/api/errors'
import type { AskVerdict } from '@/api/types'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'

import { ApprovalDialog } from './chat/approval-dialog'
import { TurnCard } from './chat/turn-card'
import { ModelSelect } from './chat/model-select'
import { REFUSAL_TEXT, STOPPED_TEXT, WITHDRAWN_TEXT, type Turn } from './chat/format'

/** 选中的模型存本地：这是"本机偏好"，不该占一个后端字段。 */
const MODEL_STORAGE_KEY = 'mindos.chat.modelId'

function readStoredModelId(): number | null {
  try {
    const raw = localStorage.getItem(MODEL_STORAGE_KEY)
    if (!raw) {
      return null
    }
    const parsed = Number(raw)
    return Number.isFinite(parsed) && parsed > 0 ? parsed : null
  } catch {
    // 隐私模式下 localStorage 会抛异常，不值得为此让页面挂掉。
    return null
  }
}

function newId(): string {
  return Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8)
}

/**
 * 把后端的 messages 还原成界面上的"一问一答"。
 *
 * 后端一条消息一行；界面按 user 开新 turn，紧随其后的 assistant 填进 answer。
 */
function turnsFromMessages(messages: ChatMessage[]): Turn[] {
  const turns: Turn[] = []
  let current: Turn | null = null
  for (const message of messages) {
    if (message.role === 'user') {
      current = {
        id: 'msg-' + message.id,
        messageId: message.id,
        question: message.content,
        answer: '',
        phase: 'settled',
        hits: [],
        citations: [],
      }
      turns.push(current)
      continue
    }
    if (!current) {
      continue
    }
    current.answer = message.content
    // 后端正常返回数组；这里再兜一层，避免旧缓存/旧版本把 base64 字符串
    // 塞进来时整页崩在 citations.map。
    current.citations = Array.isArray(message.citations) ? message.citations : []
    if (message.error_code) {
      current.phase = 'failed'
      current.error = {
        code: message.error_code,
        message: message.error_message ?? '模型返回失败',
      }
    } else {
      current.phase = 'settled'
    }
  }
  return turns
}

/**
 * 对话页：多轮会话 + 流式回答。
 *
 * 每轮把"最近 16 条历史 + 当前问题"交给后端；历史、标题、消息落库都由后端负责。
 * 页面只保留当前会话的展示状态，刷新后从 messages 重新还原。
 */
export function ChatPage() {
  const queryClient = useQueryClient()
  const modelsQuery = useQuery({
    queryKey: chatModelKeys.all,
    queryFn: ({ signal }) => fetchChatModels(signal),
    staleTime: 30_000,
  })
  const models = useMemo(() => modelsQuery.data ?? [], [modelsQuery.data])

  const [selectedId, setSelectedId] = useState<number | null>(() => readStoredModelId())
  const [activeId, setActiveId] = useState<number | null>(null)
  const [turns, setTurns] = useState<Turn[]>([])
  const [input, setInput] = useState('')
  const [pending, setPending] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [decidingId, setDecidingId] = useState<number | null>(null)
  const [approvalOpen, setApprovalOpen] = useState(false)
  const [decisionText, setDecisionText] = useState('')
  const [decisionError, setDecisionError] = useState('')
  const [createError, setCreateError] = useState('')
  const abortRef = useRef<AbortController | null>(null)
  const creatingRef = useRef(false)
  // 记录已经从后端同步过 turns 的会话，避免流式结束后用旧的空列表覆盖当前内容。
  const syncedConversationRef = useRef<number | null>(null)

  const deleteMutation = useMutation({
    mutationFn: (conversationId: number) => deleteConversation(conversationId),
    onSuccess: (_result, conversationId) => {
      queryClient.removeQueries({ queryKey: conversationKeys.messages(conversationId) })
      setActiveId(null)
      setTurns([])
      setConfirmDelete(false)
      void queryClient.invalidateQueries({ queryKey: conversationKeys.all })
    },
  })

  const conversationsQuery = useQuery({
    queryKey: conversationKeys.all,
    queryFn: ({ signal }) => fetchConversations(signal),
    enabled: models.length > 0,
    staleTime: 10_000,
  })
  useEffect(() => {
    setConfirmDelete(false)
  }, [activeId])

  const messagesQuery = useQuery({
    queryKey: conversationKeys.messages(activeId ?? 0),
    queryFn: ({ signal }) => fetchMessages(activeId ?? 0, signal),
    enabled: activeId !== null,
    staleTime: 5_000,
  })

  // 审批是会话历史的一部分：按 user_message_id 挂回对应轮次。
  const approvalsQuery = useQuery({
    queryKey: approvalKeys.conversation(activeId ?? 0),
    queryFn: ({ signal }) => fetchConversationApprovals(activeId ?? 0, signal),
    enabled: activeId !== null,
    staleTime: 5_000,
  })
  const approvals = useMemo(() => approvalsQuery.data ?? [], [approvalsQuery.data])
  const pendingCount = approvals.filter((item) => item.status === 'pending').length
  const seenPendingRef = useRef<Set<number>>(new Set())

  // 新出现的 pending 审批自动弹框；用户手动关掉后，同一 id 不反复弹。
  useEffect(() => {
    if (activeId === null) {
      return
    }
    const fresh = approvals.filter(
      (item) => item.status === 'pending' && !seenPendingRef.current.has(item.id),
    )
    if (fresh.length === 0) {
      return
    }
    fresh.forEach((item) => seenPendingRef.current.add(item.id))
    setApprovalOpen(true)
  }, [activeId, approvals])

  const decide = async (id: number, decision: 'approve' | 'reject') => {
    setDecidingId(id)
    setDecisionText('')
    setDecisionError('')
    try {
      await streamApprovalDecision(id, decision, {
        onDelta: (text) => setDecisionText((prev) => prev + text),
        onDone: () => {
          void queryClient.invalidateQueries({
            queryKey: approvalKeys.conversation(activeId ?? 0),
          })
          syncedConversationRef.current = null
          void messagesQuery.refetch()
        },
        onError: (_code, message) => setDecisionError(message),
      })
    } catch (error) {
      setDecisionError(describeError(error))
    } finally {
      setDecidingId(null)
    }
  }

  // 选中的模型可能已被删除：回落到默认那条。
  useEffect(() => {
    if (models.length === 0) {
      return
    }
    if (selectedId !== null && models.some((model) => model.id === selectedId)) {
      return
    }
    const fallback = models.find((model) => model.is_default) ?? models[0]
    setSelectedId(fallback.id)
  }, [models, selectedId])

  // 没有会话时自动建一个；有会话时默认选最近更新的一条。
  useEffect(() => {
    if (activeId !== null || !conversationsQuery.isSuccess || models.length === 0) {
      return
    }
    const list = conversationsQuery.data
    if (list.length > 0) {
      setActiveId(list[0].id)
      return
    }
    if (creatingRef.current) {
      return
    }
    creatingRef.current = true
    void (async () => {
      try {
        const conv = await createConversation()
        setCreateError('')
        setActiveId(conv.id)
        setTurns([])
        await queryClient.invalidateQueries({ queryKey: conversationKeys.all })
      } catch (error) {
        setCreateError(describeError(error))
      } finally {
        creatingRef.current = false
      }
    })()
  }, [activeId, conversationsQuery.isSuccess, conversationsQuery.data, models.length, queryClient])

  // 历史加载完成后还原 turns；流式过程中不要用缓存覆盖正在生成的内容。
  useEffect(() => {
    if (pending || activeId === null || !messagesQuery.data) {
      return
    }
    if (syncedConversationRef.current === activeId) {
      return
    }
    syncedConversationRef.current = activeId
    setTurns(turnsFromMessages(messagesQuery.data))
  }, [activeId, messagesQuery.data, pending])

  const chooseModel = (id: number) => {
    setSelectedId(id)
    try {
      localStorage.setItem(MODEL_STORAGE_KEY, String(id))
    } catch {
      // 存不了就算了：这只影响下次打开时记不记得选择。
    }
  }

  const createNewConversation = async () => {
    if (creatingRef.current) {
      return
    }
    creatingRef.current = true
    setCreateError('')
    setPending(true)
    try {
      const conv = await createConversation()
      setActiveId(conv.id)
      setTurns([])
      await queryClient.invalidateQueries({ queryKey: conversationKeys.all })
    } catch (error) {
      setCreateError(describeError(error))
    } finally {
      setPending(false)
      creatingRef.current = false
    }
  }

  const run = async (question: string) => {
    const conversationId = activeId
    if (conversationId === null) {
      return
    }
    const id = newId()
    setTurns((prev) => [
      ...prev,
      { id, question, answer: '', phase: 'streaming', hits: [], citations: [] },
    ])
    setPending(true)

    const controller = new AbortController()
    abortRef.current = controller
    const patch = (next: (turn: Turn) => Turn) => {
      setTurns((prev) => prev.map((turn) => (turn.id === id ? next(turn) : turn)))
    }

    try {
      await sendConversationMessage(
        conversationId,
        { question, chatModelId: selectedId ?? undefined },
        {
          onRetrieval: (hits) =>
            patch((turn) => {
              const byNumber = new Map(turn.hits.map((item) => [item.n, item]))
              for (const item of hits) {
                byNumber.set(item.n, item)
              }
              return {
                ...turn,
                hits: [...byNumber.values()].sort((a, b) => a.n - b.n),
              }
            }),
          onDelta: (text) => patch((turn) => ({ ...turn, answer: turn.answer + text })),
          onVerdict: (status, reason) => patch((turn) => applyVerdict(turn, status, reason)),
          onCitation: (items) => patch((turn) => ({ ...turn, citations: items })),
          onError: (code, message) =>
            patch((turn) => ({ ...turn, phase: 'failed', error: { code, message } })),
          onDone: (payload) => {
            patch((turn) => ({
              ...turn,
              messageId: payload.user_message_id ?? turn.messageId,
              done: payload,
              phase: turn.phase === 'streaming' ? 'settled' : turn.phase,
            }))
            if (payload.awaiting_approval) {
              void queryClient.invalidateQueries({
                queryKey: approvalKeys.conversation(conversationId),
              })
            }
            // 只刷新会话列表（标题/更新时间）；不重拉 messages，
            // 否则刚流式生成的内容会被一次空缓存覆盖。
            void queryClient.invalidateQueries({ queryKey: conversationKeys.all })
          },
        },
        controller.signal,
      )
    } catch (error) {
      const aborted = error instanceof ApiError && error.kind === 'aborted'
      patch((turn) => {
        if (aborted) {
          return {
            ...turn,
            phase: 'settled',
            answer: turn.answer === '' ? STOPPED_TEXT : turn.answer,
          }
        }
        return {
          ...turn,
          phase: 'failed',
          error: { code: 'client', message: describeError(error) },
        }
      })
    } finally {
      setPending(false)
      abortRef.current = null
    }
  }

  const submit = () => {
    const question = input.trim()
    if (question === '' || pending || activeId === null) {
      return
    }
    setInput('')
    void run(question)
  }

  const stop = () => abortRef.current?.abort()

  const noModel = modelsQuery.isSuccess && models.length === 0
  const conversations = conversationsQuery.data ?? []

  return (
    <div className="mx-auto flex min-h-full w-full max-w-3xl flex-col gap-5">
      <PageHeader
        title="对话"
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => setApprovalOpen(true)}>
              审批{pendingCount > 0 ? ` ${pendingCount}` : ''}
            </Button>
            <select
              aria-label="选择会话"
              className="border-input bg-background h-8 w-40 rounded-md border px-2 text-sm sm:w-52"
              value={activeId ?? ''}
              onChange={(event) => {
                const next = Number(event.target.value)
                setActiveId(Number.isFinite(next) && next > 0 ? next : null)
                setTurns([])
              }}
              disabled={pending || conversations.length === 0}
            >
              {conversations.length === 0 ? <option value="">新会话</option> : null}
              {conversations.map((conv) => (
                <option key={conv.id} value={conv.id}>
                  {conv.title || '新会话'}
                </option>
              ))}
            </select>
            {activeId !== null ? (
              confirmDelete ? (
                <>
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => deleteMutation.mutate(activeId)}
                    disabled={pending || deleteMutation.isPending}
                  >
                    确认删除
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setConfirmDelete(false)}
                    disabled={deleteMutation.isPending}
                  >
                    取消
                  </Button>
                </>
              ) : (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setConfirmDelete(true)}
                  disabled={pending}
                >
                  <Trash2 aria-hidden="true" />
                  删除会话
                </Button>
              )
            ) : null}
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void createNewConversation()}
            disabled={pending || models.length === 0}
            >
              <Plus aria-hidden="true" />
              新会话
            </Button>
          </>
        }
      />

      {modelsQuery.isPending ? (
        <div role="status" className="py-8">
          <span className="sr-only">正在加载对话模型</span>
          <Skeleton className="mx-auto h-5 w-40" />
        </div>
      ) : modelsQuery.isError ? (
        <ErrorState title="读取对话模型失败" description={describeError(modelsQuery.error)} onRetry={() => void modelsQuery.refetch()} retrying={modelsQuery.isFetching} />
      ) : noModel ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-md bg-muted px-4 py-3">
          <p className="text-sm">未配置对话模型</p>
          <Link to="/settings" className="text-primary text-sm font-medium underline underline-offset-4">
            去设置
          </Link>
        </div>
      ) : null}

      <section aria-label="会话消息" className="flex-1 space-y-5">
        {createError ? <ErrorState title="新建会话失败" description={createError} /> : null}
        {deleteMutation.error ? (
          <ErrorState title="删除会话失败" description={describeError(deleteMutation.error)} />
        ) : null}
        {conversationsQuery.isError ? (
          <ErrorState title="读取会话失败" description={describeError(conversationsQuery.error)} onRetry={() => void conversationsQuery.refetch()} retrying={conversationsQuery.isFetching} />
        ) : activeId !== null && messagesQuery.isError ? (
          <ErrorState title="读取消息失败" description={describeError(messagesQuery.error)} onRetry={() => void messagesQuery.refetch()} retrying={messagesQuery.isFetching} />
        ) : turns.length === 0 && models.length > 0 && !createError ? (
          activeId === null || messagesQuery.isPending ? (
            <div role="status" className="py-8">
              <span className="sr-only">正在加载会话</span>
              <Skeleton className="mx-auto h-5 w-32" />
            </div>
          ) : <EmptyState title="暂无消息" />
        ) : null}

        {turns.map((turn) => (
          <TurnCard key={turn.id} turn={turn} onStop={stop} />
        ))}
      </section>

      <form
        aria-label="发送消息"
        className="space-y-2 rounded-xl border border-input bg-card p-2"
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
          <Textarea
            value={input}
            onChange={(event) => setInput(event.target.value)}
            onKeyDown={(event) => {
              if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
                event.preventDefault()
                submit()
              }
            }}
            placeholder="输入问题…"
            rows={3}
            className="min-h-16 border-transparent bg-transparent shadow-none"
            maxLength={500}
            disabled={pending || models.length === 0 || activeId === null}
            aria-label="输入问题"
            aria-keyshortcuts="Meta+Enter Control+Enter"
          />
          <div className="flex flex-wrap items-center gap-2 px-1">
            <ModelSelect models={models} value={selectedId} onChange={chooseModel} disabled={pending} />
            {input.length >= 450 ? <p className="text-muted-foreground text-xs tabular-nums">{input.length}/500</p> : null}
            <Button
              type="submit"
              className="ml-auto"
              disabled={pending || input.trim() === '' || models.length === 0 || activeId === null}
            >
              {pending ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : (
                <SendHorizontal aria-hidden="true" />
              )}
              {pending ? '生成中' : '发送'}
            </Button>
          </div>
      </form>

      <ApprovalDialog
        open={approvalOpen}
        approvals={approvals}
        decidingId={decidingId}
        streamText={decisionText}
        error={decisionError}
        onClose={() => setApprovalOpen(false)}
        onDecision={decide}
      />
    </div>
  )
}

/**
 * 把后端裁决翻成界面阶段。
 */
function applyVerdict(turn: Turn, status: AskVerdict, reason: string): Turn {
  switch (status) {
    case 'answered':
      return { ...turn, verdict: status, reason }
    case 'refused':
      return { ...turn, verdict: status, reason, phase: 'refused', answer: turn.answer || REFUSAL_TEXT }
    case 'ungrounded':
      return { ...turn, verdict: status, reason, phase: 'withdrawn', answer: WITHDRAWN_TEXT }
    default:
      return { ...turn, verdict: status, reason }
  }
}
