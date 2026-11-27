import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Brain, Plus, Search } from 'lucide-react'
import { useState } from 'react'

import { describeError } from '@/api/errors'
import { fetchMemoryCandidates, fetchMemorySettings, fetchPreferences, memoryKeys, updateMemorySettings, type MemorySettings } from '@/api/memory'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'

import { MemoryCandidateCard } from './memory/candidate-card'
import { MemoryEditor } from './memory/memory-editor'
import { PreferenceCard } from './memory/preference-card'
import { categoryLabel, memoryCategories, preferenceValueLabel } from './memory/preferences'

const modes = [{ value: 'auto', label: '自动记忆' }, { value: 'suggest', label: '仅建议' }, { value: 'off', label: '关闭记忆' }]
const tabs = [{ value: 'profile', label: '个人画像' }, { value: 'all', label: '全部记忆' }, { value: 'pending', label: '待确认' }] as const

export function MemoryPage() {
  const client = useQueryClient()
  const [tab, setTab] = useState<(typeof tabs)[number]['value']>('profile')
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('all')
  const [creating, setCreating] = useState(false)
  const saved = useQuery({ queryKey: memoryKeys.preferences, queryFn: ({ signal }) => fetchPreferences(signal), refetchInterval: 5_000 })
  const candidates = useQuery({ queryKey: memoryKeys.candidates, queryFn: ({ signal }) => fetchMemoryCandidates(signal), refetchInterval: 5_000 })
  const settings = useQuery({ queryKey: memoryKeys.settings(), queryFn: ({ signal }) => fetchMemorySettings(signal) })
  const mode = useMutation({ mutationFn: (value: string) => updateMemorySettings({ mode: value as MemorySettings['mode'] }), onSuccess: () => client.invalidateQueries({ queryKey: ['memory-settings'] }) })
  const entries = saved.data ?? []
  const active = entries.filter((item) => item.status === 'active' && (!item.expires_at || Date.parse(item.expires_at) > saved.dataUpdatedAt))
  const filtered = (tab === 'profile' ? active : entries).filter((item) => (category === 'all' || item.category === category) && `${preferenceValueLabel(item.key, item.value)} ${item.scope} ${categoryLabel(item.category)}`.toLowerCase().includes(search.trim().toLowerCase()))
  const groups = [...new Set(filtered.map((item) => item.category))]

  return (
    <div className="mx-auto w-full max-w-6xl space-y-6 px-4 py-6 sm:px-8 sm:py-10">
      <header className="flex flex-wrap items-center justify-between gap-4">
        <div><p className="text-muted-foreground mb-2 text-xs font-medium tracking-widest">PERSONAL MEMORY</p><h1 className="text-3xl font-semibold tracking-tight">记忆</h1><p className="text-muted-foreground mt-2 text-sm">随着对话逐步了解你，每一条记忆都由你掌控。</p></div>
        <Button onClick={() => setCreating(true)}><Plus aria-hidden="true" />新增记忆</Button>
      </header>
      <section className="bg-card grid gap-5 rounded-2xl border p-5 sm:grid-cols-[1fr_auto] sm:items-center">
        <div className="flex gap-4"><div className="bg-primary/10 text-primary flex size-11 shrink-0 items-center justify-center rounded-xl"><Brain className="size-6" aria-hidden="true" /></div><div><h2 className="font-semibold">Lore 对你的了解</h2><p className="text-muted-foreground mt-1 text-sm">明确的新信息可自动保存，修改与不确定的内容等待确认。</p><div className="mt-3 flex flex-wrap gap-5 text-sm"><span><strong className="text-xl font-semibold">{active.length}</strong> 条有效记忆</span><span><strong className="text-xl font-semibold">{candidates.data?.length ?? 0}</strong> 项待确认</span></div></div></div>
        <div className="space-y-2">{settings.isPending ? <Skeleton className="h-9 w-36" /> : settings.isError ? <Button variant="outline" onClick={() => void settings.refetch()}>重试读取记忆模式</Button> : <Select ariaLabel="记忆模式" options={modes} value={settings.data.mode} onChange={(value) => mode.mutate(value)} disabled={mode.isPending} />}{mode.error ? <p role="alert" className="text-destructive text-xs">{describeError(mode.error)}</p> : null}<p className="text-muted-foreground text-xs">关闭后不读写对话记忆。</p></div>
      </section>
      <nav aria-label="记忆视图" className="flex gap-2 border-b">{tabs.map((entry) => <Button key={entry.value} variant="ghost" aria-pressed={tab === entry.value} className={tab === entry.value ? 'border-primary text-foreground rounded-none border-b-2' : 'text-muted-foreground'} onClick={() => setTab(entry.value)}>{entry.label}{entry.value === 'pending' && candidates.data?.length ? ` ${candidates.data.length}` : ''}</Button>)}</nav>
      {tab !== 'pending' ? <section aria-label={tab === 'profile' ? '个人画像' : '全部记忆'} className="space-y-4">
        <div className="flex flex-wrap gap-3"><div className="relative min-w-40 flex-1"><Search className="text-muted-foreground absolute left-3 top-2.5 size-4" aria-hidden="true" /><Input className="pl-9" aria-label="搜索记忆" placeholder="搜索内容、范围或类别" value={search} onChange={(event) => setSearch(event.target.value)} /></div><Select ariaLabel="筛选类别" options={[{ value: 'all', label: '所有类别' }, ...memoryCategories]} value={category} onChange={setCategory} /></div>
        {saved.isPending ? <Skeleton className="h-32 w-full" /> : saved.isError ? <ErrorState title="加载记忆失败" description={describeError(saved.error)} onRetry={() => void saved.refetch()} retrying={saved.isFetching} /> : filtered.length === 0 ? <div className="text-muted-foreground rounded-xl border border-dashed py-14 text-center text-sm">{search || category !== 'all' ? '没有符合条件的记忆' : '还没有记忆。可以手动添加，也可以从日常对话开始。'}</div> : tab === 'profile' ? groups.map((group) => <div key={group} className="space-y-3"><h2 className="text-sm font-semibold">{categoryLabel(group)}</h2><div className="grid gap-3 lg:grid-cols-2">{filtered.filter((item) => item.category === group).map((item) => <PreferenceCard key={item.id} item={item} now={saved.dataUpdatedAt} />)}</div></div>) : filtered.map((item) => <PreferenceCard key={item.id} item={item} now={saved.dataUpdatedAt} />)}
      </section> : <section aria-label="待确认" className="space-y-3">
        <p className="text-muted-foreground text-sm">确认后才会进入长期记忆；也可以修改内容或拒绝。</p>
        {candidates.isPending ? <Skeleton className="h-24 w-full" /> : candidates.isError ? <ErrorState title="加载建议失败" description={describeError(candidates.error)} onRetry={() => void candidates.refetch()} retrying={candidates.isFetching} /> : candidates.data.length === 0 ? <div className="text-muted-foreground rounded-xl border border-dashed py-14 text-center text-sm">暂无待确认建议</div> : candidates.data.map((item) => <MemoryCandidateCard key={item.id} item={item} currentValue={entries.find((entry) => entry.id === item.target_id || entry.key === item.key)?.value} />)}
      </section>}
      {creating ? <MemoryEditor onClose={() => setCreating(false)} /> : null}
    </div>
  )
}
