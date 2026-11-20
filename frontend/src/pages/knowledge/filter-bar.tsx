import { Filter, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import type { DocumentFilters, DocumentStatus } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { statusOptions } from './format'

const emptyFilters: DocumentFilters = { collection: '', tag: '', status: '' }

/**
 * 列表筛选栏。
 *
 * 条件是"点筛选才生效"而不是随输入变化：每敲一个字就发一次请求既浪费，
 * 也会让列表在输入过程中反复跳动。输入中只是草稿，提交后才成为查询条件。
 */
export function FilterBar({
  filters,
  onChange,
}: {
  filters: DocumentFilters
  onChange: (next: DocumentFilters) => void
}) {
  const [collection, setCollection] = useState(filters.collection)
  const [tag, setTag] = useState(filters.tag)
  const [status, setStatus] = useState<DocumentStatus | ''>(filters.status)

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    onChange({ collection: collection.trim(), tag: tag.trim(), status })
  }

  const reset = () => {
    setCollection('')
    setTag('')
    setStatus('')
    onChange(emptyFilters)
  }

  const isFiltered = filters.collection !== '' || filters.tag !== '' || filters.status !== ''

  return (
    <form aria-label="文档筛选" className="flex flex-wrap items-end gap-2" onSubmit={submit}>
      <label className="flex flex-col gap-1 text-xs">
        分类
        <Input value={collection} onChange={(event) => setCollection(event.target.value)} aria-label="按分类筛选" className="h-8 w-24 px-2 sm:w-32" />
      </label>
      <label className="flex flex-col gap-1 text-xs">
        标签
        <Input value={tag} onChange={(event) => setTag(event.target.value)} aria-label="按标签筛选" className="h-8 w-24 px-2 sm:w-32" />
      </label>
      <label className="flex flex-col gap-1 text-xs">
        状态
        <select
          value={status}
          onChange={(event) => setStatus(event.target.value as DocumentStatus | '')}
          aria-label="按状态筛选"
          className="border-input bg-background h-8 rounded-md border px-2 text-sm"
        >
          {statusOptions.map((option) => (
            <option key={option.value || 'all'} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </label>
      <Button type="submit" variant="outline" size="sm">
        <Filter aria-hidden="true" />
        筛选
      </Button>
      {isFiltered ? (
        <Button type="button" variant="ghost" size="sm" onClick={reset}>
          <X aria-hidden="true" />
          清除筛选
        </Button>
      ) : null}
    </form>
  )
}
