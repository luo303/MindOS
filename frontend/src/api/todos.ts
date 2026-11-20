import { apiRequest } from '@/api/client'

export type TodoStatus = 'open' | 'in_progress' | 'done' | 'cancelled'

export type Todo = {
  id: number
  title: string
  description: string
  status: TodoStatus
  priority: number
  due_at?: string | null
  source: 'manual' | 'ai'
  source_ref: Record<string, unknown>
  approval_id?: number | null
  created_at: string
  updated_at: string
  completed_at?: string | null
}

export type CreateTodoInput = {
  title: string
  description?: string
  priority?: number
  due_at?: string | null
}

export type UpdateTodoInput = {
  title?: string
  description?: string
  status?: TodoStatus
  priority?: number
  due_at?: string | null
  clear_due?: boolean
}

export const todoKeys = {
  all: ['todos'] as const,
  list: (status: string) => ['todos', 'list', status] as const,
}

export async function fetchTodos(status = '', signal?: AbortSignal): Promise<Todo[]> {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  const data = await apiRequest<{ items: Todo[] | null }>(`/todos${query}`, { signal })
  return data.items ?? []
}

export function createTodo(input: CreateTodoInput): Promise<Todo> {
  return apiRequest<Todo>('/todos', { method: 'POST', body: input })
}

export function updateTodo(id: number, input: UpdateTodoInput): Promise<Todo> {
  return apiRequest<Todo>(`/todos/${id}`, { method: 'PATCH', body: input })
}

export function deleteTodo(id: number): Promise<{ deleted: boolean }> {
  return apiRequest<{ deleted: boolean }>(`/todos/${id}`, { method: 'DELETE' })
}
