import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, Plus, SendHorizontal, Settings2, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router'

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
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'

import { ChatEmptyState, TurnCard } from './chat/turn-card'
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
    current.phase = 'settled'
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
  const navigate = useNavigate()
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
    onError: (error) => {
      console.warn('delete conversation failed', error)
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
        setActiveId(conv.id)
        setTurns([])
        await queryClient.invalidateQueries({ queryKey: conversationKeys.all })
      } catch {
        // 建会话失败时保持空态；下一次用户点"新会话"会重试。
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
    setPending(true)
    try {
      const conv = await createConversation()
      setActiveId(conv.id)
      setTurns([])
      await queryClient.invalidateQueries({ queryKey: conversationKeys.all })
    } catch (error) {
      setTurns([])
      console.warn('create conversation failed', error)
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
              done: payload,
              phase: turn.phase === 'streaming' ? 'settled' : turn.phase,
            }))
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
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
      <PageHeader
        title="对话"
        description="多轮会话；回答按需检索知识库，每句标注来源，没有依据会直接说没有。"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <select
              aria-label="选择会话"
              className="border-input bg-background h-9 max-w-52 rounded-md border px-2 text-sm"
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
                  删除
                </Button>
              )
            ) : null}
            <Button
              variant="outline"
              size="sm"
              onClick={() => void createNewConversation()}
              disabled={pending || noModel}
            >
              <Plus aria-hidden="true" />
              新会话
            </Button>
            <ModelSelect
              models={models}
              value={selectedId}
              onChange={chooseModel}
              disabled={pending || models.length === 0}
            />
          </div>
        }
      />

      {modelsQuery.isPending ? (
        <Skeleton className="h-9 w-48" />
      ) : noModel ? (
        <div className="border-border bg-muted/40 flex flex-wrap items-center justify-between gap-3 rounded-lg border px-3 py-2">
          <p className="text-sm">还没有可用的对话模型配置，对话无法开始。</p>
          <Button variant="outline" size="sm" onClick={() => navigate('/settings')}>
            <Settings2 aria-hidden="true" />
            去设置
          </Button>
        </div>
      ) : null}

      {turns.length === 0 && !noModel ? <ChatEmptyState /> : null}

      <div className="space-y-4">
        {turns.map((turn) => (
          <TurnCard key={turn.id} turn={turn} onRetry={() => void run(turn.question)} onStop={stop} />
        ))}
      </div>

      <Card>
        <CardContent className="space-y-2 pt-4">
          <Textarea
            value={input}
            onChange={(event) => setInput(event.target.value)}
            onKeyDown={(event) => {
              if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
                event.preventDefault()
                submit()
              }
            }}
            placeholder="继续这个会话，⌘/Ctrl + Enter 发送"
            rows={3}
            maxLength={500}
            disabled={pending || noModel || modelsQuery.isPending || activeId === null}
            aria-label="输入问题"
          />
          <div className="flex items-center justify-between gap-3">
            <p className="text-muted-foreground text-xs">
              {input.length}/500 · 最近 16 条对话会作为上下文
            </p>
            <Button
              onClick={submit}
              disabled={pending || input.trim() === '' || noModel || activeId === null}
            >
              {pending ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : (
                <SendHorizontal aria-hidden="true" />
              )}
              {pending ? '生成中' : '发送'}
            </Button>
          </div>
        </CardContent>
      </Card>
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
