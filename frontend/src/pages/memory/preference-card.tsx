import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router'

import { ApiError, describeError } from '@/api/errors'
import { fetchMemoryEvidence, fetchMemoryVersions, memoryKeys, updatePreference, type Preference } from '@/api/memory'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

import { categoryLabel, memorySourceLink, preferenceValueLabel } from './preferences'
import { MemoryEditor } from './memory-editor'

export function PreferenceCard({ item, now }: { item: Preference; now: number }) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [history, setHistory] = useState(false)
  const save = useMutation({
    mutationFn: (patch: { value?: string; status?: Preference['status'] }) =>
      updatePreference(item, patch),
    onSuccess: async () => {
      setEditing(false)
      await queryClient.invalidateQueries({ queryKey: memoryKeys.preferences })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        setEditing(false)
        void queryClient.invalidateQueries({ queryKey: memoryKeys.preferences })
      }
    },
  })

  return (
    <article className="bg-card border-border space-y-3 rounded-xl border p-4 shadow-xs">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1 space-y-2">
          <h3 className="text-muted-foreground text-xs font-medium">{categoryLabel(item.category)} · {item.scope || '全局'}{item.pinned ? ' · 优先使用' : ''}</h3>
          <p className="text-sm leading-relaxed break-words whitespace-pre-wrap">{preferenceValueLabel(item.key, item.value)}</p>
          <p className="text-muted-foreground text-xs">{item.status === 'archived' ? '已归档' : item.expires_at && Date.parse(item.expires_at) <= now ? '已过期' : '使用中'} · 第 {item.version} 版{item.expires_at ? ` · 至 ${new Date(item.expires_at).toLocaleDateString()}` : ''}</p>
        </div>
        {!editing ? (
          <div className="flex flex-wrap gap-1">
            <Button size="sm" variant="outline" disabled={save.isPending} onClick={() => { save.reset(); setEditing(true) }}>
              编辑
            </Button>
            {item ? (
              <>
                <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => save.mutate({ status: item.status === 'active' ? 'archived' : 'active' })}>
                  {item.status === 'active' ? '归档' : '恢复'}
                </Button>
                <Button size="sm" variant="ghost" aria-expanded={history} onClick={() => setHistory(!history)}>版本与来源</Button>
              </>
            ) : null}
          </div>
        ) : null}
      </div>
      {editing ? <MemoryEditor item={item} onClose={() => setEditing(false)} /> : null}
      {save.error ? <p role="alert" className="text-destructive text-sm">{describeError(save.error)}</p> : null}
      {history ? <><VersionList item={item} /><EvidenceList item={item} /></> : null}
    </article>
  )
}

function EvidenceList({ item }: { item: Preference }) {
  const evidence = useQuery({ queryKey: memoryKeys.evidence(item.id), queryFn: ({ signal }) => fetchMemoryEvidence(item.id, signal) })
  if (evidence.isPending) return <Skeleton className="h-8 w-full" />
  if (evidence.isError) return <p role="alert" className="text-sm">{describeError(evidence.error)} <Button variant="ghost" size="sm" onClick={() => void evidence.refetch()}>重试</Button></p>
  return <div className="border-t pt-3 space-y-2 text-sm"><h4 className="font-medium">相关来源</h4>{evidence.data.length === 0 ? <p className="text-muted-foreground">暂无原文来源</p> : evidence.data.map((row) => <blockquote key={`${row.version}:${row.message_id}`} className="border-l-2 pl-3 text-muted-foreground break-words">{row.excerpt} <Link className="underline" to={memorySourceLink(row.conversation_id, row.message_id)}>第 {row.version} 版来源</Link></blockquote>)}</div>
}

function VersionList({ item }: { item: Preference }) {
  const versions = useInfiniteQuery({
    queryKey: memoryKeys.versions(item.id), initialPageParam: 0,
    queryFn: ({ signal, pageParam }) => fetchMemoryVersions(item.id, pageParam, signal),
    getNextPageParam: (last) => last.length === 50 ? last.at(-1)?.version : undefined,
  })
  if (versions.isPending) return <Skeleton className="h-7 w-full" />
  if (versions.isError) return <p role="alert" className="text-destructive text-sm">{describeError(versions.error)} <Button size="sm" variant="outline" onClick={() => void versions.refetch()}>重试</Button></p>
  return (
    <div className="border-border border-t pt-3 text-sm">
      <ol className="text-muted-foreground space-y-2">
        {versions.data.pages.flat().map((version) => (
          <li key={version.version} className="flex flex-wrap items-baseline gap-x-2">
            <span>第 {version.version} 版 · {preferenceValueLabel(item.key, version.value)} · {version.automatic ? '自动记忆' : version.status === 'active' ? '生效' : '归档'}</span>
            <time dateTime={version.created_at}>{new Date(version.created_at).toLocaleString()}</time>
            {version.message_id && version.conversation_id ? (
              <Link className="text-foreground underline underline-offset-2" to={memorySourceLink(version.conversation_id, version.message_id)}>查看来源</Link>
            ) : version.source === 'chat' ? <span>原会话已删除</span> : <span>手动编辑</span>}
          </li>
        ))}
      </ol>
      {versions.hasNextPage ? <Button className="mt-2" size="sm" variant="ghost" disabled={versions.isFetchingNextPage} onClick={() => void versions.fetchNextPage()}>更早版本</Button> : null}
    </div>
  )
}
