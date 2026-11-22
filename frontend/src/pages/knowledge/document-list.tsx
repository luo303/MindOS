import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useRef } from 'react'

import type { DocumentView } from '@/api/types'
import { SlidingPill } from '@/components/sliding-pill'
import { Button } from '@/components/ui/button'

import { formatDate, PAGE_SIZE, sourceLabel, statusMeta } from './format'

/**
 * 文档列表。
 *
 * 只负责渲染与"点了哪一条"，取数与分页状态都留在页面入口：
 * 列表组件一旦自己取数，就和页面的筛选条件、选中态脱钩，容易各查各的。
 */
export function DocumentList({
  items,
  total,
  page,
  activeId,
  onSelect,
  onPageChange,
}: {
  items: DocumentView[]
  total: number
  page: number
  activeId: number | null
  onSelect: (id: number) => void
  onPageChange: (page: number) => void
}) {
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const listRef = useRef<HTMLUListElement | null>(null)

  return (
    <div className="space-y-2">
      <ul ref={listRef} className="relative isolate space-y-1">
        <SlidingPill
          containerRef={listRef}
          active={activeId}
          revision={items.map((item) => item.id).join(',')}
          selector={'[aria-current="true"]'}
          className="conversation-pill"
        />
        {items.map((doc) => (
          <li key={doc.id}>
            <button
              type="button"
              onClick={() => onSelect(doc.id)}
              aria-current={doc.id === activeId ? 'true' : undefined}
              className={
                'relative z-10 flex w-full flex-col gap-0.5 rounded-xl px-3 py-2.5 text-left transition-colors ' +
                (doc.id === activeId
                  ? 'text-foreground'
                  : 'text-muted-foreground hover:bg-white/45 dark:hover:bg-white/5')
              }
            >
              <span className="block w-full truncate text-sm font-medium">{doc.title}</span>
              <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs">
                <span>{sourceLabel[doc.source_type]}</span>
                <span aria-hidden="true">·</span>
                <span>{doc.status === 'ready' ? formatDate(doc.created_at) : statusMeta[doc.status].label}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>

      {total > PAGE_SIZE ? (
        <div className="flex flex-wrap items-center justify-between gap-1 border-t pt-2">
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
            <ChevronLeft aria-hidden="true" />
            上一页
          </Button>
          <p className="text-muted-foreground text-xs">
            第 {page} / {pageCount} 页 · 共 {total} 条
          </p>
          <Button
            variant="outline"
            size="sm"
            disabled={page >= pageCount}
            onClick={() => onPageChange(page + 1)}
          >
            下一页
            <ChevronRight aria-hidden="true" />
          </Button>
        </div>
      ) : null}
    </div>
  )
}
