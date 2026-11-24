import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'

import { approvalKeys, fetchConversationApprovals, streamApprovalDecision } from '@/api/approvals'
import { chatModelKeys, fetchChatModels } from '@/api/chat-models'
import {
  conversationKeys,
  createConversation,
  deleteConversation,
  fetchConversations,
  fetchMessages,
  sendConversationMessage,
  type Conversation,
} from '@/api/conversations'
import { ApiError, describeError } from '@/api/errors'

import { ChatWorkspace, type ConversationMenu } from './chat/chat-workspace'
import {
  applyVerdict,
  newId,
  readStoredConversationId,
  readStoredModelId,
  rememberConversationId,
  rememberModelId,
  turnsFromMessages,
  withLiveTurn,
} from './chat/conversation-state'
import {
  clearLive,
  liveControllerOf,
  liveTurnOf,
  publishLive,
  subscribeLive,
} from './chat/live'
import { STOPPED_TEXT, type Turn } from './chat/format'

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
  const [activeId, setActiveId] = useState<number | null>(() => readStoredConversationId())
  const [turns, setTurns] = useState<Turn[]>([])
  const [creating, setCreating] = useState(false)
  const liveTurn = useSyncExternalStore(
    subscribeLive,
    () => liveTurnOf(activeId),
    () => null,
  )
  // 只代表“当前会话正在生成”；别的会话在生成不应该阻塞这里。
  const generating = creating || liveTurn !== null
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [contextMenu, setContextMenu] = useState<ConversationMenu | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const [decidingId, setDecidingId] = useState<number | null>(null)
  const [decisionError, setDecisionError] = useState('')
  const [createError, setCreateError] = useState('')
  const creatingRef = useRef(false)
  const activeIdRef = useRef(activeId)
  // 用户主动点“新对话”后进入空白草稿，不要被自动选择逻辑选回旧会话。
  const draftRef = useRef(false)
  // 记录已经从后端同步过 turns 的会话，避免流式结束后用旧的空列表覆盖当前内容。
  const syncedConversationRef = useRef<number | null>(null)

  const deleteMutation = useMutation({
    mutationFn: (conversationId: number) => deleteConversation(conversationId),
    onSuccess: (_result, conversationId) => {
      queryClient.removeQueries({ queryKey: conversationKeys.messages(conversationId) })
      // 先把它从列表缓存里移除，避免 activeId 置空后自动选回刚删掉的会话。
      queryClient.setQueryData<Conversation[]>(conversationKeys.all, (old) =>
        old?.filter((conv) => conv.id !== conversationId),
      )
      setActiveId((current) => (current === conversationId ? null : current))
      setTurns((current) => (activeId === conversationId ? [] : current))
      // 删除当前会话后允许自动选下一条；如果删的是别的，不影响当前。
      if (activeId === conversationId) {
        draftRef.current = false
        syncedConversationRef.current = null
        rememberConversationId(null)
      }
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
    const conversationId = pendingApproval.conversation_id ?? activeId
    if (conversationId === null) {
      return
    }
    const approvalId = pendingApproval.id
    const messageId = pendingApproval.user_message_id ?? undefined
    const partial = pendingApproval.partial_answer ?? ''
    const live = liveTurnOf(conversationId)
    const local = turns.find((turn) => turn.messageId === messageId)
    const question =
      live?.question ??
      local?.question ??
      messagesQuery.data?.find((message) => message.id === messageId)?.content ??
      '审批操作'
    const base = live ?? local
    let current: Turn = {
      ...(base ?? {
        id: 'approval-' + approvalId,
        messageId,
        question,
        hits: [],
        citations: [],
      }),
      messageId,
      question,
      answer: (base?.answer ?? '') || partial,
      phase: 'streaming',
      done: undefined,
      error: undefined,
    }
    const controller = new AbortController()
    setDecidingId(approvalId)
    setDecisionError('')
    // 后端在开始 resume 前已把 approval 从 pending 改成 approved；
    // 尽快刷新，避免续跑时底部仍显示可再次点击的审批面板。
    void queryClient.invalidateQueries({
      queryKey: approvalKeys.conversation(conversationId),
    })
    const publish = (turn: Turn) => {
      publishLive(conversationId, turn, controller)
      // 只更新当前正在看的会话，不能把 A 的审批输出追加到 B。
      if (activeIdRef.current === conversationId) {
        setTurns((prev) => withLiveTurn(prev, turn))
      }
    }
    publish(current)

    const patch = (next: (turn: Turn) => Turn) => {
      current = next(current)
      publish(current)
    }

    try {
      await streamApprovalDecision(
        approvalId,
        decision,
        {
          onDelta: (text) => patch((turn) => ({ ...turn, answer: turn.answer + text })),
          onDone: (payload) => {
            patch((turn) => ({ ...turn, done: payload, phase: 'settled' }))
            void queryClient.invalidateQueries({
              queryKey: approvalKeys.conversation(conversationId),
            })
            void queryClient.invalidateQueries({
              queryKey: conversationKeys.messages(conversationId),
              refetchType: 'none',
            })
            void queryClient.invalidateQueries({ queryKey: conversationKeys.all })
          },
          onError: (code, message, _retryable, detail, requestId) => {
            patch((turn) => ({
              ...turn,
              phase: 'failed',
              error: { code, message, detail, requestId },
            }))
            if (activeIdRef.current === conversationId) {
              setDecisionError(message)
            }
            void queryClient.invalidateQueries({
              queryKey: conversationKeys.messages(conversationId),
              refetchType: 'none',
            })
          },
        },
        controller.signal,
      )
    } catch (error) {
      const aborted = error instanceof ApiError && error.kind === 'aborted'
      patch((turn) =>
        aborted
          ? {
              ...turn,
              phase: 'settled',
              answer: turn.answer === '' ? STOPPED_TEXT : turn.answer,
            }
          : {
              ...turn,
              phase: 'failed',
              error: { code: 'approval', message: describeError(error) },
            },
      )
      if (!aborted && activeIdRef.current === conversationId) {
        setDecisionError(describeError(error))
      }
      void queryClient.invalidateQueries({
        queryKey: conversationKeys.messages(conversationId),
        refetchType: 'none',
      })
    } finally {
      clearLive(conversationId, controller)
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

  useEffect(() => {
    activeIdRef.current = activeId
    rememberConversationId(activeId)
  }, [activeId])

  // 优先恢复上次的会话；失效时回落到最近一条。草稿状态不自动选择。
  useEffect(() => {
    if (!conversationsQuery.isSuccess || models.length === 0) {
      return
    }
    const list = conversationsQuery.data
    if (list.length === 0) {
      if (activeId !== null) {
        setActiveId(null)
      }
      return
    }
    if (activeId !== null) {
      if (!list.some((conv) => conv.id === activeId)) {
        const fallback = readStoredConversationId()
        const target = fallback && list.some((conv) => conv.id === fallback) ? fallback : list[0].id
        syncedConversationRef.current = null
        setActiveId(target)
      }
      return
    }
    if (draftRef.current) {
      return
    }
    const stored = readStoredConversationId()
    const target = stored && list.some((conv) => conv.id === stored) ? stored : list[0].id
    syncedConversationRef.current = null
    setActiveId(target)
  }, [activeId, conversationsQuery.isSuccess, conversationsQuery.data, models.length])

  // 历史加载完成后还原 turns；流式过程中不要用缓存覆盖正在生成的内容。
  useEffect(() => {
    // 有 liveTurn 时也要合并历史；没有 liveTurn 的会话不能因为
    // 另一个会话正在生成而被挡在门外。
    if (activeId === null || !messagesQuery.data) {
      return
    }
    if (syncedConversationRef.current === activeId) {
      return
    }
    syncedConversationRef.current = activeId
    const restored = turnsFromMessages(messagesQuery.data)
    setTurns(liveTurn ? withLiveTurn(restored, liveTurn) : restored)
  }, [activeId, messagesQuery.data, liveTurn])

  // 切页回来时，本地缓存的历史可能还没有正在生成的 assistant 消息；
  // liveTurn 是模块级的，重新挂载后把它合并回来，流式内容继续可见。
  useEffect(() => {
    if (!liveTurn || activeId === null) {
      return
    }
    setTurns((prev) => withLiveTurn(prev, liveTurn))
  }, [activeId, liveTurn])

  // 刷新或首次进入 pending 审批时，live store 是空的；
  // 从 approval.partial_answer 把审批前正文补回对应轮次。
  useEffect(() => {
    const partial = pendingApproval?.partial_answer?.trim()
    if (!partial || liveTurn || activeId === null) {
      return
    }
    const messageId = pendingApproval?.user_message_id
    setTurns((prev) =>
      prev.map((turn) =>
        turn.messageId === messageId && turn.answer === ''
          ? { ...turn, answer: partial, phase: 'settled' }
          : turn,
      ),
    )
  }, [activeId, liveTurn, pendingApproval, messagesQuery.data])

  // 审批也可能在任务页/另一个标签页被处理；当 approval 已不再 pending 时，
  // 清掉聊天页里停留的审批前 live turn，避免回来看到过期的“待审批”正文。
  useEffect(() => {
    const approvalId = liveTurn?.done?.approval_id
    if (!approvalId || activeId === null || !liveTurn?.done?.awaiting_approval) {
      return
    }
    const approval = approvals.find((item) => item.id === approvalId)
    if (!approval || approval.status === 'pending' || approval.status === 'approved') {
      return
    }
    const controller = liveControllerOf(activeId)
    if (!controller) {
      return
    }
    clearLive(activeId, controller)
    // 当前页面还在看这个会话，直接取回审批后的最终消息；
    // 若这里只标脏，live 被清掉后会短暂只剩审批前正文。
    void queryClient.invalidateQueries({
      queryKey: conversationKeys.messages(activeId),
    })
  }, [activeId, approvals, liveTurn, queryClient])

  const chooseModel = (id: number) => {
    setSelectedId(id)
    try {
      rememberModelId(id)
    } catch {
      // 存不了就算了：这只影响下次打开时记不记得选择。
    }
  }

  const startNewConversation = () => {
    if (generating || activeId === null) {
      return
    }
    draftRef.current = true
    syncedConversationRef.current = null
    rememberConversationId(null)
    setActiveId(null)
    setTurns([])
    setCreateError('')
    setSidebarOpen(false)
  }

  const run = async (question: string) => {
    let conversationId = activeId
    // 空白草稿不落库；用户发出第一句话时才创建会话。
    if (conversationId === null) {
      if (creatingRef.current) {
        return
      }
      creatingRef.current = true
      setCreating(true)
      try {
        const conv = await createConversation()
        conversationId = conv.id
        draftRef.current = false
        syncedConversationRef.current = null
        setCreateError('')
        setActiveId(conv.id)
        queryClient.setQueryData<Conversation[]>(conversationKeys.all, (old) =>
          old ? [conv, ...old] : [conv],
        )
      } catch (error) {
        setCreateError(describeError(error))
        setCreating(false)
        creatingRef.current = false
        return
      }
      creatingRef.current = false
      setCreating(false)
    }

    // 本地已经有一个正在生成的轮次；后续 messages 刷新不要用旧缓存覆盖它。
    syncedConversationRef.current = conversationId
    const id = newId()
    const controller = new AbortController()
    let current: Turn = { id, question, answer: '', phase: 'streaming', hits: [], citations: [] }
    setTurns((prev) => [...prev, current])
    publishLive(conversationId, current, controller)

    const patch = (next: (turn: Turn) => Turn) => {
      current = next(current)
      const updated = current
      setTurns((prev) => prev.map((turn) => (turn.id === id ? updated : turn)))
      publishLive(conversationId, updated, controller)
    }

    let awaitingApproval = false
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
          onError: (code, message, _retryable, detail, requestId) =>
            patch((turn) => ({
              ...turn,
              phase: 'failed',
              error: { code, message, detail, requestId },
            })),
          onDone: (payload) => {
            patch((turn) => ({
              ...turn,
              messageId: payload.user_message_id ?? turn.messageId,
              done: payload,
              phase: turn.phase === 'streaming' ? 'settled' : turn.phase,
            }))
            if (payload.awaiting_approval) {
              awaitingApproval = true
              void queryClient.invalidateQueries({
                queryKey: approvalKeys.conversation(conversationId),
              })
            }
            // 会话列表立即更新；messages 只标脏、不立刻重拉，
            // 避免覆盖正在显示的流式内容，但下次回来时会重新取最终消息。
            void queryClient.invalidateQueries({ queryKey: conversationKeys.all })
            void queryClient.invalidateQueries({
              queryKey: conversationKeys.messages(conversationId),
              refetchType: 'none',
            })
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
      // 审批暂停不是流结束：保留 live turn，切页/刷新到审批阶段时
      // 还要靠它（以及 approval.partial_answer）恢复审批前正文。
      if (!awaitingApproval) {
        clearLive(conversationId, controller)
      }
    }
  }

  const submit = (raw: string) => {
    const question = raw.trim()
    if (question === '' || generating) {
      return
    }
    void run(question)
  }

  const stop = () => {
    if (activeId !== null) {
      liveControllerOf(activeId)?.abort()
    }
  }

  function selectConversation(id: number) {
    draftRef.current = false
    syncedConversationRef.current = null
    setActiveId(id)
    setTurns([])
    setSidebarOpen(false)
  }

  const noModel = modelsQuery.isSuccess && models.length === 0
  const conversations = conversationsQuery.data ?? []
  const activeConversation = conversations.find((conv) => conv.id === activeId) ?? null
  const showEmpty =
    turns.length === 0 &&
    !generating &&
    !createError &&
    !pendingApproval &&
    (activeId === null || (messagesQuery.isSuccess && !messagesQuery.isError))

  return (
    <ChatWorkspace
      activeId={activeId}
      activeConversation={activeConversation}
      conversations={conversations}
      conversationsQuery={conversationsQuery}
      models={models}
      modelsQuery={modelsQuery}
      selectedId={selectedId}
      chooseModel={chooseModel}
      turns={turns}
      messagesQuery={messagesQuery}
      generating={generating}
      showEmpty={showEmpty}
      noModel={noModel}
      createError={createError}
      pendingApproval={pendingApproval}
      decidingId={decidingId}
      decisionError={decisionError}
      decide={decide}
      submit={submit}
      stop={stop}
      startNewConversation={startNewConversation}
      onSelectConversation={selectConversation}
      sidebarOpen={sidebarOpen}
      setSidebarOpen={setSidebarOpen}
      contextMenu={contextMenu}
      setContextMenu={setContextMenu}
      deleteTarget={deleteTarget}
      setDeleteTarget={setDeleteTarget}
      deleteMutation={deleteMutation}
    />
  )
}
