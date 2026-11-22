import { X } from 'lucide-react'
import { useEffect, type ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

type Props = {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  className?: string
}

/** 居中弹框；用于导入文件、筛选等短流程。 */
export function Modal({ open, title, onClose, children, className }: Props) {
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
    <div
      className="fixed inset-0 z-[80] flex items-start justify-center bg-black/40 p-4 pt-[10vh]"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose()
        }
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={cn('bg-background w-full max-w-lg rounded-2xl border p-4 shadow-2xl', className)}
      >
        <header className="mb-3 flex items-center justify-between gap-3">
          <h2 className="text-base font-semibold">{title}</h2>
          <Button variant="ghost" size="icon" aria-label="关闭弹框" onClick={onClose}>
            <X aria-hidden="true" />
          </Button>
        </header>
        {children}
      </div>
    </div>
  )
}
