import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router'

import { describeError } from '@/api/errors'
import { decideMemoryCandidate, memoryKeys, type MemoryCandidate } from '@/api/memory'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'

import { memorySourceLink, preferenceLabels, preferenceValueLabel, preferenceValues } from './preferences'

export function MemoryCandidateCard({ item, currentValue, inChat = false }: {
  item: MemoryCandidate
  currentValue?: string
  inChat?: boolean
}) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(item.value)
  const decision = useMutation({
    mutationFn: (change: { action: 'accept' | 'reject'; value?: string }) =>
      decideMemoryCandidate(item.id, change.action, change.value),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: memoryKeys.candidates }),
        queryClient.invalidateQueries({ queryKey: memoryKeys.preferences }),
      ])
    },
  })

  return (
    <article className="bg-card border-border space-y-3 rounded-xl border p-4" aria-label="记忆建议">
      <div>
        <p className="text-muted-foreground text-xs">记忆建议 · 确认后才生效</p>
        <p className="mt-1 text-sm font-medium">
          {preferenceLabels[item.key]}：{currentValue ? `${preferenceValueLabel(item.key, currentValue)} → ` : ''}
          {preferenceValueLabel(item.key, item.value)}
        </p>
      </div>
      {!inChat ? (
        <blockquote className="text-muted-foreground border-border border-l-2 pl-3 text-sm break-words">
          {item.evidence}
          <Link className="text-foreground ml-2 underline underline-offset-2" to={memorySourceLink(item.conversation_id, item.message_id)}>
            查看来源
          </Link>
        </blockquote>
      ) : null}
      {editing ? (
        <Select ariaLabel="建议值" value={value} onChange={setValue} options={preferenceValues[item.key]} align="left" disabled={decision.isPending} />
      ) : null}
      {decision.error ? <p role="alert" className="text-destructive text-sm">{describeError(decision.error)}</p> : null}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" disabled={decision.isPending} onClick={() => decision.mutate({ action: 'accept', value: editing ? value : undefined })}>
          {editing ? '保存修改' : '保存为记忆'}
        </Button>
        <Button size="sm" variant="outline" disabled={decision.isPending} onClick={() => setEditing(!editing)}>
          {editing ? '取消编辑' : '编辑后保存'}
        </Button>
        <Button size="sm" variant="ghost" disabled={decision.isPending} onClick={() => decision.mutate({ action: 'reject' })}>
          不保存
        </Button>
      </div>
    </article>
  )
}
