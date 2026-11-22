import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, Menu, MessageSquare, Plus, SendHorizontal, Trash2, X } from 'lucide-react'
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
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'

import { ApprovalPanel } from './chat/approval-panel'
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
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [contextMenu, setContextMenu] = useState<{ id: number; x: number; y: number } | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const [decidingId, setDecidingId] = useState<number | null>(null)
  const [decisionError, setDecisionError] = useState('')
  const [createError, setCreateError] = useState('')
  const abortRef = useRef<AbortController | null>(null)
  const creatingRef = useRef(false)
  const endRef = useRef<HTMLDivElement | null>(null)
  // 记录已经从后端同步过 turns 的会话，避免流式结束后用旧的空列表覆盖当前内容。
  const syncedConversationRef = useRef<number | null>(null)

  const deleteMutation = useMutation({
    mutationFn: (conversationId: number) => deleteConversation(conversationId),
    onSuccess: (_result, conversationId) => {
      queryClient.removeQueries({ queryKey: conversationKeys.messages(conversationId) })
      setActiveId((current) => (current === conversationId ? null : current))
      setTurns((current) => (activeId === conversationId ? [] : current))
      setDeleteTarget(null)
      setContextMenu(null)
      void queryClient.invalidateQueries({ queryKey: conversationKeys.all })
    },
  })

  const conversationsQuery = useQuery({
    queryKey: conversationKeys.all,
    queryFn: ({ signal }) => fetchConversations(signal),
    enabled: models.length > 0,
    staleTime: 10_000,
  })
  // 点击任意位置/按 Esc 关闭右键菜单。
  useEffect(() => {
    if (!contextMenu) {
      return
    }
    const close = () => setContextMenu(null)
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') close()
    }
    window.addEventListener('mousedown', close)
    window.addEventListener('keydown', escape)
    return () => {
      window.removeEventListener('mousedown', close)
      window.removeEventListener('keydown', escape)
    }
  }, [contextMenu])

  // 新消息/流式增量都贴到底部；历史长时不会停在半空。
  useEffect(() => {
    endRef.current?.scrollIntoView?.({ behavior: pending ? 'auto' : 'smooth', block: 'end' })
  }, [turns, pending])

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
  const pendingApproval = approvals.find((item) => item.status === 'pending') ?? null

  const decide = async (decision: 'approve' | 'reject') => {
    if (!pendingApproval) {
      return
    }
    const id = pendingApproval.id
    const messageId = pendingApproval.user_message_id ?? undefined
    setDecidingId(id)
    setDecisionError('')
    const patchApprovalTurn = (next: (turn: Turn) => Turn) => {
      setTurns((prev) => {
        const index = prev.findIndex((turn) => turn.messageId === messageId)
        if (index >= 0) {
          return prev.map((turn, i) => (i === index ? next({ ...turn, phase: 'streaming' }) : turn))
        }
        const question =
          messagesQuery.data?.find((message) => message.id === messageId)?.content ?? '审批操作'
        return [
          ...prev,
          next({
            id: 'approval-' + id,
            messageId,
            question,
            answer: '',
            phase: 'streaming',
            hits: [],
            citations: [],
          }),
        ]
      })
    }
    // 先建立/切换到流式轮次，首个 delta 之前显示骨架，而不是把正文塞进审批框。
    patchApprovalTurn((turn) => turn)
    try {
      await streamApprovalDecision(id, decision, {
        onDelta: (text) => patchApprovalTurn((turn) => ({ ...turn, answer: turn.answer + text })),
        onDone: (payload) => {
          patchApprovalTurn((turn) => ({ ...turn, done: payload, phase: 'settled' }))
          void queryClient.invalidateQueries({
            queryKey: approvalKeys.conversation(activeId ?? 0),
          })
          syncedConversationRef.current = null
          void messagesQuery.refetch()
        },
        onError: (_code, message) => setDecisionError(message),
      })
    } catch (error) {
      patchApprovalTurn((turn) => ({
        ...turn,
        phase: 'failed',
        error: { code: 'approval', message: describeError(error) },
      }))
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
        setSidebarOpen(false)
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
  const activeConversation = conversations.find((conv) => conv.id === activeId) ?? null

  return (
    <div className="relative flex h-full min-h-0 overflow-hidden">
      {sidebarOpen ? (
        <button
          type="button"
          className="fixed inset-0 z-20 bg-black/30 md:hidden"
          aria-label="关闭会话列表"
          onClick={() => setSidebarOpen(false)}
        />
      ) : null}

      <aside
        className={
          'bg-muted/20 absolute inset-y-0 left-0 z-30 flex w-64 shrink-0 flex-col border-r transition-transform md:static md:translate-x-0 ' +
          (sidebarOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0')
        }
        aria-label="历史会话"
      >
        <div className="border-b p-3">
          <Button
            className="w-full justify-start"
            size="sm"
            onClick={() => void createNewConversation()}
            disabled={pending || models.length === 0}
          >
            <Plus aria-hidden="true" />
            新对话
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-2">
          {conversationsQuery.isPending ? (
            <div className="space-y-2 p-1">
              <Skeleton className="h-8 w-full" />
              <Skeleton className="h-8 w-5/6" />
              <Skeleton className="h-8 w-4/6" />
            </div>
          ) : conversationsQuery.isError ? (
            <p className="text-destructive p-2 text-xs">{describeError(conversationsQuery.error)}</p>
          ) : conversations.length === 0 ? (
            <p className="text-muted-foreground p-2 text-xs">还没有历史会话</p>
          ) : (
            <ul className="space-y-0.5">
              {conversations.map((conv) => (
                <li key={conv.id}>
                  <button
                    type="button"
                    className={
                      'hover:bg-muted flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm transition-colors ' +
                      (conv.id === activeId ? 'bg-muted font-medium' : 'text-muted-foreground')
                    }
                    onClick={() => {
                      setActiveId(conv.id)
                      setTurns([])
                      setSidebarOpen(false)
                    }}
                    onContextMenu={(event) => {
                      event.preventDefault()
                      setContextMenu({
                        id: conv.id,
                        x: Math.min(event.clientX, window.innerWidth - 180),
                        y: Math.min(event.clientY, window.innerHeight - 60),
                      })
                    }}
                    title="右键删除会话"
                  >
                    <MessageSquare className="size-4 shrink-0" aria-hidden="true" />
                    <span className="min-w-0 flex-1 truncate">{conv.title || '新会话'}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-3">
          <Button
            variant="ghost"
            size="icon"
            className="md:hidden"
            aria-label="打开会话列表"
            onClick={() => setSidebarOpen(true)}
          >
            <Menu aria-hidden="true" />
          </Button>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-semibold">
              {activeConversation?.title || (activeId === null ? '新对话' : '对话')}
            </p>
            <p className="text-muted-foreground truncate text-[11px]">
              {pendingApproval ? '等待审批，完成后才能继续' : pending ? '生成中…' : '右键左侧会话可删除'}
            </p>
          </div>
          <ModelSelect models={models} value={selectedId} onChange={chooseModel} disabled={pending} />
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto" aria-label="会话消息">
          <div className="mx-auto w-full max-w-3xl px-4 py-6">
            {createError ? <ErrorState title="新建会话失败" description={createError} /> : null}
            {deleteMutation.error ? (
              <ErrorState title="删除会话失败" description={describeError(deleteMutation.error)} />
            ) : null}
            {modelsQuery.isPending ? (
              <div role="status" className="py-8">
                <span className="sr-only">正在加载对话模型</span>
                <Skeleton className="mx-auto h-5 w-40" />
              </div>
            ) : modelsQuery.isError ? (
              <ErrorState
                title="读取对话模型失败"
                description={describeError(modelsQuery.error)}
                onRetry={() => void modelsQuery.refetch()}
                retrying={modelsQuery.isFetching}
              />
            ) : noModel ? (
              <div className="glass-surface flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                <p className="text-sm">未配置对话模型</p>
                <Link to="/settings" className="text-primary text-sm font-medium underline underline-offset-4">
                  去设置
                </Link>
              </div>
            ) : null}

            {activeId !== null && messagesQuery.isError ? (
              <ErrorState
                title="读取消息失败"
                description={describeError(messagesQuery.error)}
                onRetry={() => void messagesQuery.refetch()}
                retrying={messagesQuery.isFetching}
              />
            ) : null}

            {messagesQuery.isPending && activeId !== null ? (
              <div role="status" className="glass-surface py-8">
                <span className="sr-only">正在加载会话</span>
                <Skeleton className="mx-auto h-5 w-32" />
              </div>
            ) : turns.length === 0 && !createError ? (
              <div className="flex flex-col items-center justify-center px-4 py-24 text-center">
                <div className="bg-muted mb-4 flex size-12 items-center justify-center rounded-full">
                  <MessageSquare className="size-6" aria-hidden="true" />
                </div>
                <h2 className="text-lg font-semibold">开始新的对话</h2>
                <p className="text-muted-foreground mt-2 max-w-sm text-sm">
                  问知识库里的内容，或者让 AI 结合资料创建待办。审批会显示在下方输入区。
                </p>
              </div>
            ) : null}

            <div className="space-y-5">
              {turns.map((turn) => (
                <TurnCard key={turn.id} turn={turn} onStop={stop} />
              ))}
            </div>
            <div ref={endRef} />
          </div>
        </div>

        <div className="bg-background/95 shrink-0 border-t">
          <div className="mx-auto w-full max-w-3xl p-3">
            {pendingApproval ? (
              <ApprovalPanel
                approval={pendingApproval}
                deciding={decidingId === pendingApproval.id}
                error={decisionError}
                onDecision={decide}
              />
            ) : (
              <form
                aria-label="发送消息"
                className="glass-surface space-y-2 p-2.5"
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
                  {input.length >= 450 ? (
                    <p className="text-muted-foreground text-xs tabular-nums">{input.length}/500</p>
                  ) : null}
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
            )}
          </div>
        </div>
      </div>

      {contextMenu ? (
        <div
          className="bg-background fixed z-50 w-40 rounded-md border p-1 shadow-lg"
          style={{ left: contextMenu.x, top: contextMenu.y }}
          onMouseDown={(event) => event.stopPropagation()}
        >
          <button
            type="button"
            className="text-destructive hover:bg-muted flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm"
            onClick={() => {
              setDeleteTarget(contextMenu.id)
              setContextMenu(null)
            }}
          >
            <Trash2 className="size-4" aria-hidden="true" />
            删除会话
          </button>
        </div>
      ) : null}

      {deleteTarget !== null ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) {
              setDeleteTarget(null)
            }
          }}
        >
          <div role="dialog" aria-modal="true" aria-label="删除会话" className="bg-background w-full max-w-sm rounded-lg border p-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <h2 className="text-base font-semibold">删除会话？</h2>
                <p className="text-muted-foreground mt-1 text-xs">消息和审批记录会一起删除，无法恢复。</p>
              </div>
              <Button variant="ghost" size="icon" aria-label="取消删除" onClick={() => setDeleteTarget(null)}>
                <X aria-hidden="true" />
              </Button>
            </div>
            <div className="mt-4 flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => setDeleteTarget(null)}>
                取消
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => deleteMutation.mutate(deleteTarget)}
                disabled={deleteMutation.isPending}
              >
                {deleteMutation.isPending ? '删除中…' : '删除'}
              </Button>
            </div>
          </div>
        </div>
      ) : null}
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
