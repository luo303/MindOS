import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ListChecks, Pencil, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'

import {
  createTodo,
  deleteTodo,
  fetchTodos,
  todoKeys,
  updateTodo,
  type CreateTodoInput,
  type Todo,
  type TodoStatus,
  type UpdateTodoInput,
} from '@/api/todos'
import { describeError } from '@/api/errors'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { PageHeader } from '@/components/page-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'

const statusOptions: Array<{ value: string; label: string }> = [
  { value: '', label: '全部' },
  { value: 'open', label: '待办' },
  { value: 'in_progress', label: '进行中' },
  { value: 'done', label: '已完成' },
  { value: 'cancelled', label: '已取消' },
]

const priorityLabels: Record<number, string> = { 1: '高', 2: '中', 3: '低' }
const statusLabels: Record<TodoStatus, string> = {
  open: '待办',
  in_progress: '进行中',
  done: '已完成',
  cancelled: '已取消',
}

type Draft = {
  id: number | null
  title: string
  description: string
  priority: number
  due: string
}

const emptyDraft: Draft = { id: null, title: '', description: '', priority: 2, due: '' }

function toDateTimeInput(iso?: string | null): string {
  if (!iso) {
    return ''
  }
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    date.getFullYear() +
    '-' +
    pad(date.getMonth() + 1) +
    '-' +
    pad(date.getDate()) +
    'T' +
    pad(date.getHours()) +
    ':' +
    pad(date.getMinutes())
  )
}

function toISO(local: string): string | null {
  if (!local) {
    return null
  }
  const date = new Date(local)
  return Number.isNaN(date.getTime()) ? null : date.toISOString()
}

function formatDue(iso?: string | null): string {
  if (!iso) {
    return '无截止时间'
  }
  return new Date(iso).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function TasksPage() {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState('')
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  const [formOpen, setFormOpen] = useState(false)
  const [confirmId, setConfirmId] = useState<number | null>(null)

  const listQuery = useQuery({
    queryKey: todoKeys.list(status),
    queryFn: ({ signal }) => fetchTodos(status, signal),
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: todoKeys.all })

  const saveMutation = useMutation({
    mutationFn: async (d: Draft) => {
      const payload: CreateTodoInput = {
        title: d.title.trim(),
        description: d.description.trim(),
        priority: d.priority,
        due_at: toISO(d.due),
      }
      if (d.id === null) {
        return createTodo(payload)
      }
      const patch: UpdateTodoInput = { ...payload }
      return updateTodo(d.id, patch)
    },
    onSuccess: () => {
      setDraft(emptyDraft)
      setFormOpen(false)
      void invalidate()
    },
  })

  const statusMutation = useMutation({
    mutationFn: ({ id, next }: { id: number; next: TodoStatus }) => updateTodo(id, { status: next }),
    onSuccess: () => void invalidate(),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => deleteTodo(id),
    onSuccess: () => {
      setConfirmId(null)
      void invalidate()
    },
  })

  const openCreate = () => {
    setDraft(emptyDraft)
    setFormOpen(true)
  }

  const openEdit = (todo: Todo) => {
    setDraft({
      id: todo.id,
      title: todo.title,
      description: todo.description,
      priority: todo.priority,
      due: toDateTimeInput(todo.due_at),
    })
    setFormOpen(true)
  }

  const items = listQuery.data ?? []

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
      <PageHeader
        title="任务"
        actions={
          <div className="flex items-center gap-2">
            <select
              aria-label="筛选状态"
              className="border-input bg-background h-9 rounded-md border px-2 text-sm"
              value={status}
              onChange={(event) => setStatus(event.target.value)}
            >
              {statusOptions.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
            <Button size="sm" onClick={openCreate}>
              <Plus aria-hidden="true" />
              新建待办
            </Button>
          </div>
        }
      />

      <p className="text-muted-foreground text-sm">
        手动记录待办；也可以让 AI 结合知识库创建，经你审批后落库。
      </p>

      {formOpen ? (
        <Card>
          <CardContent className="space-y-3 pt-5">
            <Input
              aria-label="待办标题"
              placeholder="要做什么？"
              value={draft.title}
              onChange={(event) => setDraft({ ...draft, title: event.target.value })}
              maxLength={200}
            />
            <Textarea
              aria-label="待办描述"
              placeholder="补充说明（可选）"
              value={draft.description}
              onChange={(event) => setDraft({ ...draft, description: event.target.value })}
              rows={3}
            />
            <div className="flex flex-wrap items-center gap-3">
              <label className="flex items-center gap-2 text-sm">
                优先级
                <select
                  className="border-input bg-background h-9 rounded-md border px-2"
                  value={draft.priority}
                  onChange={(event) => setDraft({ ...draft, priority: Number(event.target.value) })}
                >
                  <option value={1}>高</option>
                  <option value={2}>中</option>
                  <option value={3}>低</option>
                </select>
              </label>
              <label className="flex items-center gap-2 text-sm">
                截止
                <input
                  aria-label="截止时间"
                  type="datetime-local"
                  className="border-input bg-background h-9 rounded-md border px-2 text-sm"
                  value={draft.due}
                  onChange={(event) => setDraft({ ...draft, due: event.target.value })}
                />
              </label>
            </div>
            {saveMutation.error ? (
              <p className="text-destructive text-sm">{describeError(saveMutation.error)}</p>
            ) : null}
            <div className="flex gap-2">
              <Button
                size="sm"
                onClick={() => saveMutation.mutate(draft)}
                disabled={saveMutation.isPending || draft.title.trim() === ''}
              >
                {draft.id === null ? '创建' : '保存'}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setFormOpen(false)
                  setDraft(emptyDraft)
                }}
                disabled={saveMutation.isPending}
              >
                取消
              </Button>
            </div>
          </CardContent>
        </Card>
      ) : null}

      {listQuery.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-20 w-full" />
        </div>
      ) : listQuery.error ? (
        <ErrorState
          title="读取待办失败"
          description={describeError(listQuery.error)}
          onRetry={() => void listQuery.refetch()}
          retrying={listQuery.isFetching}
        />
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed">
          <EmptyState title="还没有待办" />
        </div>
      ) : (
        <ul className="space-y-2">
          {items.map((todo) => (
            <li key={todo.id}>
              <Card>
                <CardContent className="flex items-start gap-3 py-4">
                  <button
                    type="button"
                    aria-label={todo.status === 'done' ? '标记为未完成' : '标记为完成'}
                    className={
                      todo.status === 'done'
                        ? 'bg-primary text-primary-foreground mt-0.5 flex size-5 shrink-0 items-center justify-center rounded border'
                        : 'border-input mt-0.5 flex size-5 shrink-0 items-center justify-center rounded border'
                    }
                    onClick={() =>
                      statusMutation.mutate({
                        id: todo.id,
                        next: todo.status === 'done' ? 'open' : 'done',
                      })
                    }
                    disabled={statusMutation.isPending}
                  >
                    {todo.status === 'done' ? <Check className="size-3" aria-hidden="true" /> : null}
                  </button>

                  <div className="min-w-0 flex-1 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span
                        className={
                          todo.status === 'done'
                            ? 'text-muted-foreground text-sm line-through'
                            : 'text-sm font-medium'
                        }
                      >
                        {todo.title}
                      </span>
                      <Badge variant="outline">{priorityLabels[todo.priority] ?? '中'}</Badge>
                      <Badge variant="secondary">{statusLabels[todo.status]}</Badge>
                      {todo.source === 'ai' ? <Badge>AI 创建</Badge> : null}
                    </div>
                    {todo.description ? (
                      <p className="text-muted-foreground text-xs whitespace-pre-wrap">{todo.description}</p>
                    ) : null}
                    <p className="text-muted-foreground text-xs">截止：{formatDue(todo.due_at)}</p>
                  </div>

                  <div className="flex shrink-0 gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label="编辑待办"
                      onClick={() => openEdit(todo)}
                    >
                      <Pencil aria-hidden="true" />
                    </Button>
                    {confirmId === todo.id ? (
                      <>
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={() => deleteMutation.mutate(todo.id)}
                          disabled={deleteMutation.isPending}
                        >
                          确认
                        </Button>
                        <Button variant="ghost" size="sm" onClick={() => setConfirmId(null)}>
                          取消
                        </Button>
                      </>
                    ) : (
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label="删除待办"
                        onClick={() => setConfirmId(todo.id)}
                      >
                        <Trash2 aria-hidden="true" />
                      </Button>
                    )}
                  </div>
                </CardContent>
              </Card>
            </li>
          ))}
        </ul>
      )}

      {items.length > 0 ? (
        <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
          <ListChecks className="size-3.5" aria-hidden="true" />
          共 {items.length} 条
        </p>
      ) : null}
    </div>
  )
}
