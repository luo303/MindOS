import { useQuery } from '@tanstack/react-query'
import { Loader2, SendHorizontal, Settings2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router'

import { askStream } from '@/api/ask'
import { chatModelKeys, fetchChatModels } from '@/api/chat-models'
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
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

/**
 * 问答页：单轮流式问答，答案带可点回原文的引用。
 *
 * "单轮"指后端不维护会话上下文，每次提问都是独立一次检索 + 回答。
 * 前端仍保留本次页面里的多组问答，方便对比；刷新即清空。
 */
export function ChatPage() {
  const navigate = useNavigate()
  const modelsQuery = useQuery({
    queryKey: chatModelKeys.all,
    queryFn: ({ signal }) => fetchChatModels(signal),
    staleTime: 30_000,
  })
  // data 未到时给同一个空数组而不是每次渲染新建 []：
  // 否则 useEffect 的依赖每轮都变，fallback 逻辑会被无意义地反复触发。
  const models = useMemo(() => modelsQuery.data ?? [], [modelsQuery.data])

  const [selectedId, setSelectedId] = useState<number | null>(() => readStoredModelId())
  const [turns, setTurns] = useState<Turn[]>([])
  const [input, setInput] = useState('')
  const [pending, setPending] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  // 选中的模型可能已被删除：回落到默认那条，不留下一个指向不存在配置的下拉。
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

  const chooseModel = (id: number) => {
    setSelectedId(id)
    try {
      localStorage.setItem(MODEL_STORAGE_KEY, String(id))
    } catch {
      // 存不了就算了：这只影响下次打开时记不记得选择。
    }
  }

  const run = async (question: string) => {
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
      await askStream(
        { question, chatModelId: selectedId ?? undefined },
        {
          onRetrieval: (hits) => patch((turn) => ({ ...turn, hits })),
          onDelta: (text) => patch((turn) => ({ ...turn, answer: turn.answer + text })),
          onVerdict: (status, reason) => patch((turn) => applyVerdict(turn, status, reason)),
          onCitation: (items) => patch((turn) => ({ ...turn, citations: items })),
          onError: (code, message) =>
            patch((turn) => ({ ...turn, phase: 'failed', error: { code, message } })),
          onDone: (payload) =>
            patch((turn) => ({
              ...turn,
              done: payload,
              // 已经判成 refused / withdrawn 的不要被 done 覆盖回 settled。
              phase: turn.phase === 'streaming' ? 'settled' : turn.phase,
            })),
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
    if (question === '' || pending) {
      return
    }
    setInput('')
    void run(question)
  }

  const stop = () => abortRef.current?.abort()

  const noModel = modelsQuery.isSuccess && models.length === 0

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
      <PageHeader
        title="问答"
        description="只根据已导入的文档回答，每句标注来源；没有依据时直接说没有。"
        actions={
          <ModelSelect
            models={models}
            value={selectedId}
            onChange={chooseModel}
            disabled={pending || models.length === 0}
          />
        }
      />

      {modelsQuery.isPending ? (
        <Skeleton className="h-9 w-48" />
      ) : noModel ? (
        <div className="border-border bg-muted/40 flex flex-wrap items-center justify-between gap-3 rounded-lg border px-3 py-2">
          <p className="text-sm">还没有可用的对话模型配置，问答无法开始。</p>
          <Button variant="outline" size="sm" onClick={() => navigate('/settings')}>
            <Settings2 aria-hidden="true" />
            去设置
          </Button>
        </div>
      ) : null}

      {turns.length === 0 && !noModel ? <ChatEmptyState /> : null}

      <div className="space-y-4">
        {turns.map((turn) => (
          <TurnCard
            key={turn.id}
            turn={turn}
            onRetry={() => void run(turn.question)}
            onStop={stop}
          />
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
            placeholder="问一个关于知识库里内容的问题，⌘/Ctrl + Enter 发送"
            rows={3}
            maxLength={500}
            disabled={pending || noModel || modelsQuery.isPending}
            aria-label="输入问题"
          />
          <div className="flex items-center justify-between gap-3">
            <p className="text-muted-foreground text-xs">
              {input.length}/500 · 回答只使用知识库内容，没有依据会拒答
            </p>
            <Button onClick={submit} disabled={pending || input.trim() === '' || noModel}>
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
 *
 * ungrounded 要把正文换成撤回提示——这是"流式 + 末尾裁决"的代价，
 * 也是它唯一的代价：极少数情况下用户会看到正文被替换。
 */
function applyVerdict(turn: Turn, status: AskVerdict, reason: string): Turn {
  switch (status) {
    case 'answered':
      return { ...turn, verdict: status, reason }
    case 'refused':
      // 模型自己说无法回答时保留原话；闸门拦下的情况一个 delta 都没有，用固定文案。
      return { ...turn, verdict: status, reason, phase: 'refused', answer: turn.answer || REFUSAL_TEXT }
    case 'ungrounded':
      return { ...turn, verdict: status, reason, phase: 'withdrawn', answer: WITHDRAWN_TEXT }
    default:
      return { ...turn, verdict: status, reason }
  }
}
