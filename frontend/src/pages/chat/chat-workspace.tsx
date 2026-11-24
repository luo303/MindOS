import type { UseQueryResult } from '@tanstack/react-query'
import { Menu, MessageSquare, Plus, Trash2, X } from 'lucide-react'
import { useRef, type RefObject } from 'react'
import { Link } from 'react-router'

import type { Approval } from '@/api/approvals'
import type { Conversation } from '@/api/conversations'
import { describeError } from '@/api/errors'
import type { ChatModel } from '@/api/types'
import { ErrorState } from '@/components/error-state'
import { SlidingPill } from '@/components/sliding-pill'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

import { ApprovalPanel } from './approval-panel'
import { ChatComposer } from './chat-composer'
import type { Turn } from './format'
import { ModelSelect } from './model-select'
import { TurnCard } from './turn-card'

type QueryStatus = Pick<UseQueryResult, 'isPending' | 'isError' | 'error'>
type RetryableQueryStatus = QueryStatus & Pick<UseQueryResult, 'isFetching' | 'refetch'>

export type ConversationMenu = { id: number; x: number; y: number }

type Props = {
  activeId: number | null
  activeConversation: Conversation | null
  conversations: Conversation[]
  conversationsQuery: QueryStatus
  models: ChatModel[]
  modelsQuery: RetryableQueryStatus
  selectedId: number | null
  chooseModel: (id: number) => void
  turns: Turn[]
  endRef: RefObject<HTMLDivElement | null>
  messagesQuery: RetryableQueryStatus
  generating: boolean
  showEmpty: boolean
  noModel: boolean
  createError: string
  pendingApproval: Approval | null
  decidingId: number | null
  decisionError: string
  decide: (decision: 'approve' | 'reject') => void
  submit: (value: string) => void
  stop: () => void
  startNewConversation: () => void
  onSelectConversation: (id: number) => void
  sidebarOpen: boolean
  setSidebarOpen: (open: boolean) => void
  contextMenu: ConversationMenu | null
  setContextMenu: (menu: ConversationMenu | null) => void
  deleteTarget: number | null
  setDeleteTarget: (id: number | null) => void
  deleteMutation: { mutate: (id: number) => void; isPending: boolean; error: Error | null }
}

export function ChatWorkspace({
  activeId, activeConversation, conversations, conversationsQuery, models, modelsQuery,
  selectedId, chooseModel, turns, endRef, messagesQuery, generating, showEmpty, noModel,
  createError, pendingApproval, decidingId, decisionError, decide, submit, stop,
  startNewConversation, onSelectConversation, sidebarOpen, setSidebarOpen,
  contextMenu, setContextMenu, deleteTarget, setDeleteTarget, deleteMutation,
}: Props) {
  const conversationListRef = useRef<HTMLUListElement | null>(null)

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
            onClick={startNewConversation}
            disabled={generating || models.length === 0 || activeId === null}
          >
            <Plus aria-hidden="true" />
            新对话
          </Button>
        </div>

        <div className="chat-scroll min-h-0 flex-1 overflow-y-auto p-2">
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
            <ul ref={conversationListRef} className="relative isolate space-y-0.5">
              <SlidingPill
                containerRef={conversationListRef}
                active={activeId}
                selector={'[aria-current="true"]'}
                className="conversation-pill"
              />
              {conversations.map((conv) => (
                <li key={conv.id}>
                  <button
                    type="button"
                    aria-current={conv.id === activeId ? 'true' : undefined}
                    className={
                      'relative z-10 flex w-full items-center gap-2 rounded-xl px-2.5 py-2 text-left text-sm transition-colors ' +
                      (conv.id === activeId
                        ? 'text-foreground font-medium'
                        : 'text-muted-foreground hover:bg-white/45')
                    }
                    onClick={() => onSelectConversation(conv.id)}
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
            {pendingApproval ? (
              <p className="text-muted-foreground truncate text-[11px]">等待审批，完成后才能继续</p>
            ) : generating ? (
              <p className="text-muted-foreground truncate text-[11px]">生成中…</p>
            ) : null}
          </div>
          <ModelSelect models={models} value={selectedId} onChange={chooseModel} disabled={generating} />
        </header>

        <div
          className={
            showEmpty
              ? 'flex min-h-0 flex-1 flex-col items-center justify-center px-4 pb-16'
              : 'flex min-h-0 flex-1 flex-col'
          }
        >
          {showEmpty ? (
            <div className="mb-8 w-full max-w-2xl text-center">
              <img src="/favicon.svg" alt="" className="mx-auto mb-4 size-12" />
              <h1 className="text-3xl font-semibold tracking-tight">今天想聊什么？</h1>
              {noModel ? (
                <div className="glass-surface mt-4 flex flex-wrap items-center justify-between gap-3 px-4 py-3 text-left">
                  <p className="text-sm">未配置对话模型</p>
                  <Link to="/settings" className="text-primary text-sm font-medium underline underline-offset-4">
                    去设置
                  </Link>
                </div>
              ) : null}
            </div>
          ) : (
            <div className="chat-scroll min-h-0 flex-1 overflow-y-auto" aria-label="会话消息">
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
                ) : null}

                <div className="space-y-6">
                  {turns.map((turn) => (
                    <TurnCard key={turn.id} turn={turn} onStop={stop} />
                  ))}
                </div>
                <div ref={endRef} />
              </div>
            </div>
          )}

          <div className={showEmpty ? 'w-full max-w-2xl' : 'shrink-0 px-3 pb-3'}>
            <div className="mx-auto w-full max-w-3xl">
              {pendingApproval ? (
                <ApprovalPanel
                  approval={pendingApproval}
                  deciding={decidingId === pendingApproval.id}
                  error={decisionError}
                  onDecision={decide}
                />
              ) : (
                <ChatComposer
                  onSubmit={submit}
                  pending={generating}
                  disabled={
                    generating ||
                    models.length === 0 ||
                    (activeId !== null && messagesQuery.isPending)
                  }
                />
              )}
            </div>
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
