import { Loader2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { describeError } from '@/api/errors'
import type { CreateTodoInput, Todo } from '@/api/todos'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

const priorityOptions = [
  { value: '1', label: '高' },
  { value: '2', label: '中' },
  { value: '3', label: '低' },
]

function toDateTimeInput(iso?: string | null): string {
  if (!iso) {
    return ''
  }
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function toISO(local: string): string | null {
  if (!local) {
    return null
  }
  const date = new Date(local)
  return Number.isNaN(date.getTime()) ? null : date.toISOString()
}

type Props = {
  todo: Todo | null
  saving: boolean
  error: Error | null
  onCancel: () => void
  onSubmit: (input: CreateTodoInput) => void
}

export function TodoEditor({ todo, saving, error, onCancel, onSubmit }: Props) {
  const [title, setTitle] = useState(todo?.title ?? '')
  const [description, setDescription] = useState(todo?.description ?? '')
  const [priority, setPriority] = useState(String(todo?.priority ?? 2))
  const [due, setDue] = useState(() => toDateTimeInput(todo?.due_at))
  const canSubmit = !saving && title.trim() !== ''

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canSubmit) {
      return
    }
    onSubmit({
      title: title.trim(),
      description: description.trim(),
      priority: Number(priority),
      due_at: toISO(due),
    })
  }

  return (
    <form className="flex h-full min-h-[27rem] w-full flex-col gap-3" onSubmit={handleSubmit} aria-busy={saving}>
      <fieldset className="flex min-h-0 min-w-0 flex-1 flex-col gap-3" disabled={saving}>
        <label className="block space-y-1.5 text-sm font-medium">
          <span>待办标题</span>
          <Input value={title} onChange={(event) => setTitle(event.target.value)} placeholder="要做什么？" maxLength={200} autoFocus />
        </label>
        <label className="flex min-h-36 flex-1 flex-col gap-1.5 text-sm font-medium">
          <span>待办描述</span>
          <Textarea
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="补充说明（可选）"
            className="min-h-0 flex-1 resize-none"
          />
        </label>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5 text-sm font-medium">
            <span>优先级</span>
            <Select ariaLabel="优先级" value={priority} options={priorityOptions} onChange={setPriority} disabled={saving} className="w-full" />
          </div>
          <label className="block space-y-1.5 text-sm font-medium">
            <span>截止时间</span>
            <Input type="datetime-local" value={due} onChange={(event) => setDue(event.target.value)} />
          </label>
        </div>
      </fieldset>
      <div className="mt-auto shrink-0 space-y-3 pt-2">
        {error ? <p role="alert" className="text-destructive text-sm break-words">{describeError(error)}</p> : null}
        <div className="flex items-center justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onCancel} disabled={saving}>取消</Button>
          <Button type="submit" size="sm" disabled={!canSubmit}>
            {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
            {todo ? '保存' : '创建'}
          </Button>
        </div>
      </div>
    </form>
  )
}
