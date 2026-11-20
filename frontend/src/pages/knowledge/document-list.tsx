import { ChevronLeft, ChevronRight } from 'lucide-react'

import type { DocumentView } from '@/api/types'
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

  return (
    <div className="space-y-3">
      <ul className="space-y-0.5">
        {items.map((doc) => (
          <li key={doc.id}>
            <button
              type="button"
              onClick={() => onSelect(doc.id)}
              aria-current={doc.id === activeId ? 'true' : undefined}
              className="hover:bg-accent/60 aria-[current=true]:bg-accent bg-transparent focus-visible:outline-ring w-full space-y-1 rounded-md px-3 py-2.5 text-left transition-colors focus-visible:outline-2"
            >
              <span className="block truncate text-sm font-medium">{doc.title}</span>
              <span className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
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
