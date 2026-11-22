import { useQuery } from '@tanstack/react-query'
import { ArrowRight, Circle, FileText } from 'lucide-react'
import type { ReactNode } from 'react'
import { Link } from 'react-router'

import { documentKeys, fetchDocuments } from '@/api/documents'
import { describeError } from '@/api/errors'
import { fetchTodos, todoKeys } from '@/api/todos'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

const preview = { limit: 3, offset: 0, collection: '', tag: '', status: '' } as const
const shortDate = new Intl.DateTimeFormat('zh-CN', { month: 'short', day: 'numeric' })

export function RecentDocuments() {
  const documents = useQuery({
    queryKey: documentKeys.list(preview),
    queryFn: ({ signal }) => fetchDocuments(preview, signal),
  })

  return (
    <Activity title="最近文档" href="/knowledge" label="打开知识库">
      {documents.isPending ? <Loading /> : documents.isError ? (
        <Failed error={documents.error} retry={() => void documents.refetch()} retrying={documents.isFetching} />
      ) : documents.data.items.length ? (
        <ul className="divide-y">
          {documents.data.items.map((doc) => (
            <li key={doc.id}>
              <Link className="hover:bg-accent/60 flex items-center gap-2.5 rounded-lg px-2 py-2.5 text-sm" to={`/knowledge?doc=${doc.id}`}>
                <FileText aria-hidden="true" className="text-primary size-4 shrink-0" />
                <span className="truncate">{doc.title}</span>
                <time dateTime={doc.created_at} className="text-muted-foreground ml-auto shrink-0 text-xs">
                  {shortDate.format(new Date(doc.created_at))}
                </time>
              </Link>
            </li>
          ))}
        </ul>
      ) : <Empty>还没有文档</Empty>}
    </Activity>
  )
}

export function OpenTodos() {
  const todos = useQuery({
    queryKey: todoKeys.list(''),
    queryFn: ({ signal }) => fetchTodos('', signal),
  })
  const active = todos.data?.filter(({ status }) => status === 'open' || status === 'in_progress').slice(0, 3) ?? []

  return (
    <Activity title="待办" href="/tasks" label="打开任务">
      {todos.isPending ? <Loading /> : todos.isError ? (
        <Failed error={todos.error} retry={() => void todos.refetch()} retrying={todos.isFetching} />
      ) : active.length ? (
        <ul className="divide-y">
          {active.map((todo) => (
            <li key={todo.id} className="flex items-center gap-2.5 px-2 py-2.5 text-sm">
              <Circle aria-hidden="true" className="text-primary size-3.5 shrink-0" />
              <span className="truncate">{todo.title}</span>
              {todo.status === 'in_progress' ? <span className="text-muted-foreground ml-auto shrink-0 text-xs">进行中</span> : null}
            </li>
          ))}
        </ul>
      ) : <Empty>没有待办</Empty>}
    </Activity>
  )
}

function Activity({ title, href, label, children }: { title: string; href: string; label: string; children: ReactNode }) {
  return (
    <section className="min-w-0">
      <Card className="h-full min-h-40 gap-2 px-4 py-3 sm:px-5">
        <header className="flex items-center justify-between gap-3">
          <h2 className="font-semibold tracking-tight">{title}</h2>
          <Link to={href} aria-label={label} className="text-muted-foreground hover:bg-accent hover:text-primary grid size-9 place-items-center rounded-full">
            <ArrowRight className="size-4" aria-hidden="true" />
          </Link>
        </header>
        {children}
      </Card>
    </section>
  )
}

function Loading() {
  return <div aria-busy="true" aria-label="正在加载" className="space-y-3 px-2 py-3"><Skeleton className="h-4 w-3/4" /><Skeleton className="h-4 w-1/2" /></div>
}

function Empty({ children }: { children: string }) {
  return <p className="text-muted-foreground rounded-xl border border-dashed px-3 py-5 text-center text-sm">{children}</p>
}

function Failed({ error, retry, retrying }: { error: unknown; retry: () => void; retrying: boolean }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 px-2 py-2">
      <p role="alert" className="text-destructive min-w-0 text-sm break-words">{describeError(error)}</p>
      <Button variant="outline" size="sm" onClick={retry} disabled={retrying}>重试</Button>
    </div>
  )
}
