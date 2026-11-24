import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Pencil, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'

import {
  createTodo,
  deleteTodos,
  deleteTodo,
  fetchTodos,
  todoKeys,
  updateTodo,
  type CreateTodoInput,
  type Todo,
  type TodoStatus,
} from '@/api/todos'
import { describeError } from '@/api/errors'
import { DeleteConfirmDialog } from '@/components/delete-confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { PageHeader } from '@/components/page-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Drawer } from '@/components/ui/drawer'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'

import { TodoEditor } from './tasks/todo-editor'

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

type TodoDialog =
  | { kind: 'create' }
  | { kind: 'edit'; todo: Todo }
  | { kind: 'delete'; todo: Todo }
  | { kind: 'batch-delete'; ids: number[] }

function formatDue(iso: string): string {
  return new Date(iso).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function TasksPage() {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState('open')
  const [selected, setSelected] = useState<Set<number>>(() => new Set())
  const [dialog, setDialog] = useState<TodoDialog | null>(null)
  const isEditorOpen = dialog?.kind === 'create' || dialog?.kind === 'edit'
  const editingTodo = dialog?.kind === 'edit' ? dialog.todo : null
  const deletingTodo = dialog?.kind === 'delete' ? dialog.todo : null
  const deletingIDs = dialog?.kind === 'batch-delete' ? dialog.ids : []

  const listQuery = useQuery({
    queryKey: todoKeys.list(status),
    queryFn: ({ signal }) => fetchTodos(status, signal),
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: todoKeys.all })

  const saveMutation = useMutation({
    mutationFn: ({ id, input }: { id: number | null; input: CreateTodoInput }) =>
      id === null ? createTodo(input) : updateTodo(id, input),
    onSuccess: () => {
      setDialog(null)
      void invalidate()
    },
  })

  const statusMutation = useMutation({
    mutationFn: ({ id, next }: { id: number; next: TodoStatus }) => updateTodo(id, { status: next }),
    onSuccess: () => void invalidate(),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => deleteTodo(id),
    onSuccess: (_result, id) => {
      setDialog(null)
      setSelected((prev) => {
        const next = new Set(prev)
        next.delete(id)
        return next
      })
      void invalidate()
    },
  })

  const batchDeleteMutation = useMutation({
    mutationFn: (ids: number[]) => deleteTodos(ids),
    onSuccess: () => {
      setDialog(null)
      setSelected(new Set())
      void invalidate()
    },
  })

  function handleOpenEditor(todo?: Todo) {
    if (dialog) {
      return
    }
    saveMutation.reset()
    setDialog(todo ? { kind: 'edit', todo } : { kind: 'create' })
  }

  function handleOpenDelete(todo: Todo) {
    if (dialog) {
      return
    }
    deleteMutation.reset()
    setDialog({ kind: 'delete', todo })
  }

  function handleOpenBatchDelete() {
    if (dialog || selectedIDs.length === 0) {
      return
    }
    batchDeleteMutation.reset()
    setDialog({ kind: 'batch-delete', ids: selectedIDs })
  }

  function handleCloseDialog() {
    if (!saveMutation.isPending && !deleteMutation.isPending && !batchDeleteMutation.isPending) {
      setDialog(null)
    }
  }

  const items = listQuery.data ?? []
  const selectedIDs = items.filter((todo) => selected.has(todo.id)).map((todo) => todo.id)
  const allSelected = items.length > 0 && selectedIDs.length === items.length

  function toggleSelected(id: number) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  function toggleAll() {
    setSelected(allSelected ? new Set() : new Set(items.map((todo) => todo.id)))
  }

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
      <PageHeader
        title="任务"
        actions={
          <div className="flex items-center gap-2">
            <Select
              ariaLabel="筛选状态"
              value={status}
              options={statusOptions}
              onChange={(next) => {
                setStatus(next)
                setSelected(new Set())
              }}
              className="h-9 px-3 text-sm"
            />
            <Button size="sm" onClick={() => handleOpenEditor()}>
              <Plus aria-hidden="true" />
              新建待办
            </Button>
          </div>
        }
      />

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
        <div className="glass-surface">
          <EmptyState title={status ? '没有符合条件的待办' : '还没有待办'} />
        </div>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-3">
            <label className="text-muted-foreground flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={allSelected}
                onChange={toggleAll}
                aria-label="全选待办"
                className="accent-primary size-4"
              />
              全选
            </label>
            {selectedIDs.length > 0 ? (
              <Button variant="destructive" size="sm" onClick={handleOpenBatchDelete}>
                <Trash2 aria-hidden="true" />
                批量删除（{selectedIDs.length}）
              </Button>
            ) : null}
          </div>
          <ul className="space-y-2">
            {items.map((todo) => (
            <li key={todo.id}>
              <Card className="py-0">
                <CardContent className="flex flex-wrap items-start gap-3 px-4 py-3 sm:px-5">
                  <input
                    type="checkbox"
                    aria-label={`选择待办：${todo.title}`}
                    checked={selected.has(todo.id)}
                    onChange={() => toggleSelected(todo.id)}
                    className="accent-primary mt-0.5 size-4 shrink-0"
                  />
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

                  <div className="min-w-0 flex-1 basis-36 space-y-1">
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
                    {todo.due_at ? <p className="text-muted-foreground text-xs">截止：{formatDue(todo.due_at)}</p> : null}
                  </div>

                  <div className="ml-auto flex shrink-0 gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label="编辑待办"
                      onClick={() => handleOpenEditor(todo)}
                    >
                      <Pencil aria-hidden="true" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label="删除待办"
                      onClick={() => handleOpenDelete(todo)}
                    >
                      <Trash2 aria-hidden="true" />
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </li>
            ))}
          </ul>
        </>
      )}

      <Drawer open={isEditorOpen} title={editingTodo ? '编辑待办' : '新建待办'} onClose={handleCloseDialog}>
        <TodoEditor
          key={editingTodo ? `edit-${editingTodo.id}` : 'create'}
          todo={editingTodo}
          saving={saveMutation.isPending}
          error={saveMutation.error}
          onCancel={handleCloseDialog}
          onSubmit={(input) => saveMutation.mutate({ id: editingTodo?.id ?? null, input })}
        />
      </Drawer>

      {deletingTodo ? (
        <DeleteConfirmDialog
          title="删除待办"
          targetName={deletingTodo.title}
          pending={deleteMutation.isPending}
          error={deleteMutation.error}
          onCancel={handleCloseDialog}
          onConfirm={() => deleteMutation.mutate(deletingTodo.id)}
        />
      ) : null}

      {deletingIDs.length > 0 ? (
        <DeleteConfirmDialog
          title="批量删除待办"
          targetName={`选中的 ${deletingIDs.length} 条待办`}
          pending={batchDeleteMutation.isPending}
          error={batchDeleteMutation.error}
          onCancel={handleCloseDialog}
          onConfirm={() => batchDeleteMutation.mutate(deletingIDs)}
        />
      ) : null}
    </div>
  )
}
