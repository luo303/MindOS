import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'

import { describeError } from '@/api/errors'
import { createMemory, memoryKeys, updatePreference, type Preference } from '@/api/memory'
import { Button } from '@/components/ui/button'
import { Drawer } from '@/components/ui/drawer'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import { memoryCategories, preferenceValueLabel, preferenceValues } from './preferences'
import type { PreferenceKey } from '@/api/memory'

export function MemoryEditor({ item, onClose }: { item?: Preference; onClose: () => void }) {
  const client = useQueryClient()
  const [value, setValue] = useState(item ? preferenceValueLabel(item.key, item.value) : '')
  const [category, setCategory] = useState(item?.category ?? 'profile')
  const [scope, setScope] = useState(item?.scope ?? '')
  const [expiry, setExpiry] = useState(item?.expires_at?.slice(0, 10) ?? '')
  const [pinned, setPinned] = useState(item?.pinned ?? false)
  const legacyOptions = item ? preferenceValues[item.key as PreferenceKey] : undefined
  const save = useMutation({
    mutationFn: () => {
      const metadata = { category, scope: scope.trim(), expires_at: expiry ? new Date(`${expiry}T23:59:59`).toISOString() : null, pinned: pinned && !scope.trim() }
      const content = legacyOptions?.find((option) => option.label === value.trim())?.value ?? value.trim()
      return item ? updatePreference(item, { value: content, metadata }) : createMemory(content, metadata)
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: memoryKeys.preferences })
      onClose()
    },
  })
  return (
    <Drawer open title={item ? '编辑记忆' : '新增记忆'} onClose={onClose}>
      <form className="space-y-5" onSubmit={(event) => { event.preventDefault(); save.mutate() }}>
        <label className="block space-y-2 text-sm font-medium">记忆内容
          <Textarea value={value} onChange={(event) => setValue(event.target.value)} maxLength={500} placeholder="用一两句话描述希望 Lore 记住的信息" required />
        </label>
        <div className="space-y-2 text-sm font-medium">类别
          <Select ariaLabel="类别" value={category} options={memoryCategories.some((option) => option.value === category) ? memoryCategories : [...memoryCategories, { value: category, label: category }]} onChange={setCategory} />
        </div>
        <label className="block space-y-2 text-sm font-medium">适用范围（留空为全局）
          <Input value={scope} maxLength={120} onChange={(event) => setScope(event.target.value)} placeholder="例如 MindOS" />
        </label>
        <label className="block space-y-2 text-sm font-medium">有效期（留空为长期）
          <Input type="date" value={expiry} onChange={(event) => setExpiry(event.target.value)} />
        </label>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={pinned && !scope.trim()} disabled={!!scope.trim()} onChange={(event) => setPinned(event.target.checked)} /> 优先使用此全局记忆</label>
        {save.error ? <p role="alert" className="text-destructive text-sm">{describeError(save.error)}</p> : null}
        <div className="flex justify-end gap-2"><Button type="button" variant="outline" onClick={onClose}>取消</Button><Button type="submit" disabled={save.isPending || !value.trim()}>保存记忆</Button></div>
      </form>
    </Drawer>
  )
}
