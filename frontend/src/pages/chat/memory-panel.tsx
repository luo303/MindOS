import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Brain } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'

import { describeError } from '@/api/errors'
import { fetchMemoryCandidates, fetchMemorySettings, fetchPreferences, memoryKeys, updateMemorySettings } from '@/api/memory'
import { Button } from '@/components/ui/button'
import { Drawer } from '@/components/ui/drawer'
import { Skeleton } from '@/components/ui/skeleton'

import { MemoryCandidateCard } from '../memory/candidate-card'

export function MemoryPanel({ conversationId }: { conversationId: number }) {
  const [open, setOpen] = useState(false)
  const client = useQueryClient()
  const settings = useQuery({ queryKey: memoryKeys.settings(conversationId), queryFn: ({ signal }) => fetchMemorySettings(signal, conversationId) })
  const candidates = useQuery({ queryKey: memoryKeys.conversation(conversationId), queryFn: ({ signal }) => fetchMemoryCandidates(signal, conversationId), refetchInterval: 5_000 })
  const saved = useQuery({ queryKey: memoryKeys.preferences, queryFn: ({ signal }) => fetchPreferences(signal), enabled: (candidates.data?.length ?? 0) > 0 })
  const toggle = useMutation({ mutationFn: (off: boolean) => updateMemorySettings({ conversation_id: conversationId, conversation_off: off }), onSuccess: () => client.invalidateQueries({ queryKey: memoryKeys.settings(conversationId) }) })
  const count = candidates.data?.length ?? 0
  const body = <div className="space-y-3">
    <div className="flex flex-wrap items-center justify-between gap-2"><h2 className="font-semibold text-sm">本会话记忆</h2><Link to="/memory" className="text-muted-foreground text-xs underline underline-offset-4">管理全部</Link></div>
    {settings.isPending ? <Skeleton className="h-8 w-full" /> : settings.isError ? <Button size="sm" variant="outline" onClick={() => void settings.refetch()}>重试读取设置</Button> : <Button size="sm" className="w-full" variant="outline" aria-pressed={settings.data.conversation_off} disabled={toggle.isPending || settings.data.mode === 'off'} onClick={() => toggle.mutate(!settings.data.conversation_off)}>{settings.data.mode === 'off' ? '全局记忆已关闭' : settings.data.conversation_off ? '恢复会话记忆' : '关闭会话记忆'}</Button>}
    {toggle.error ? <p role="alert" className="text-destructive text-xs">{describeError(toggle.error)}</p> : null}
    {candidates.isPending ? <Skeleton className="h-16 w-full" /> : candidates.isError ? <Button size="sm" variant="outline" onClick={() => void candidates.refetch()}>重试读取建议</Button> : count === 0 ? <p className="text-muted-foreground text-xs leading-relaxed">暂无待确认建议。记忆在后台整理，不影响回答生成。</p> : candidates.data?.map((item) => <MemoryCandidateCard key={item.id} item={item} currentValue={saved.data?.find((entry) => entry.id === item.target_id || entry.key === item.key)?.value} />)}
  </div>
  return <>
    <aside aria-label="会话记忆" className="hidden w-72 shrink-0 overflow-y-auto border-l p-4 min-[1500px]:block">{body}</aside>
    <Button aria-label={`打开记忆面板${count ? `，${count} 项待确认` : ''}`} variant="outline" size="sm" className="bg-background absolute right-4 top-16 z-10 shadow-sm min-[1500px]:hidden" onClick={() => setOpen(true)}><Brain aria-hidden="true" />{count || '记忆'}</Button>
    <Drawer open={open} title="记忆建议" onClose={() => setOpen(false)}>{body}</Drawer>
  </>
}
