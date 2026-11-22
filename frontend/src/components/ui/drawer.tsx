import { X } from 'lucide-react'
import { useEffect, type ReactNode } from 'react'

import { Button } from '@/components/ui/button'

type Props = {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
}

/** 右侧抽屉：用于新建笔记等不需要离开当前文档的编辑面板。 */
export function Drawer({ open, title, onClose, children }: Props) {
  useEffect(() => {
    if (!open) {
      return
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        onClose()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, onClose])

  if (!open) {
    return null
  }

  return (
    <>
      <div
        className="fixed inset-0 z-[70] bg-black/35"
        role="presentation"
        onMouseDown={(event) => {
          if (event.target === event.currentTarget) {
            onClose()
          }
        }}
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="drawer-panel bg-background fixed inset-y-0 right-0 z-[80] flex w-full max-w-lg flex-col border-l shadow-2xl"
      >
        <header className="flex shrink-0 items-center justify-between gap-3 border-b px-4 py-3">
          <h2 className="text-base font-semibold">{title}</h2>
          <Button variant="ghost" size="icon" aria-label="关闭抽屉" onClick={onClose}>
            <X aria-hidden="true" />
          </Button>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto p-4">{children}</div>
      </aside>
    </>
  )
}
