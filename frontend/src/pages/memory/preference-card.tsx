import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router'

import { ApiError, describeError } from '@/api/errors'
import { createPreference, fetchMemoryVersions, memoryKeys, updatePreference, type Preference, type PreferenceKey } from '@/api/memory'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'

import { memorySourceLink, preferenceLabels, preferenceValueLabel, preferenceValues } from './preferences'

export function PreferenceCard({ preferenceKey, item }: { preferenceKey: PreferenceKey; item?: Preference }) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [history, setHistory] = useState(false)
  const save = useMutation({
    mutationFn: (patch: { value?: string; status?: Preference['status'] }) =>
      item ? updatePreference(item, patch) : createPreference(preferenceKey, patch.value ?? ''),
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
    <article className="bg-card border-border space-y-3 rounded-xl border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-medium">{preferenceLabels[preferenceKey]}</h3>
          <p className="text-muted-foreground mt-1 text-sm">
            {item ? preferenceValueLabel(preferenceKey, item.value) : '未设置'}
            {item?.status === 'archived' ? ' · 已归档，不参与对话' : null}
          </p>
        </div>
        {!editing ? (
          <div className="flex flex-wrap gap-1">
            <Button size="sm" variant="outline" disabled={save.isPending} onClick={() => { save.reset(); setDraft(item?.value ?? ''); setEditing(true) }}>
              {item ? '编辑' : '设置偏好'}
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
      {editing ? (
        <div className="flex flex-wrap items-center gap-2">
          <Select options={preferenceValues[preferenceKey]} ariaLabel={preferenceLabels[preferenceKey]} align="left" value={draft} onChange={setDraft} disabled={save.isPending} />
          <Button size="sm" disabled={!draft || save.isPending} onClick={() => save.mutate({ value: draft })}>保存偏好</Button>
          <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => setEditing(false)}>取消</Button>
        </div>
      ) : null}
      {save.error ? <p role="alert" className="text-destructive text-sm">{describeError(save.error)}</p> : null}
      {history && item ? <VersionList item={item} /> : null}
    </article>
  )
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
            <span>第 {version.version} 版 · {preferenceValueLabel(item.key, version.value)} · {version.status === 'active' ? '生效' : '归档'}</span>
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
