import { X } from 'lucide-react'
import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

type Props = {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  className?: string
  dismissible?: boolean
  descriptionId?: string
}

/** 居中弹框；用于导入文件、筛选等短流程。 */
export function Modal({ open, title, onClose, children, className, dismissible = true, descriptionId }: Props) {
  const panelRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const panel = panelRef.current
    if (!open || !panel) {
      return
    }
    const trigger = document.activeElement
    const initialFocus = panel.querySelector<HTMLElement>('[data-initial-focus]') ??
      panel.querySelector<HTMLElement>('[data-modal-close]') ?? panel
    initialFocus.focus({ preventScroll: true })
    return () => {
      if (trigger instanceof HTMLElement && trigger.isConnected) {
        trigger.focus({ preventScroll: true })
      }
    }
  }, [open])

  useEffect(() => {
    if (!open) {
      return
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && dismissible) {
        onClose()
      }
      const panel = panelRef.current
      if (event.key !== 'Tab' || !panel) {
        return
      }
      const focusable = panel.querySelectorAll<HTMLElement>(
        'button:not(:disabled), input:not(:disabled):not([type="hidden"]), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex="0"]',
      )
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (!first || !panel.contains(document.activeElement) || document.activeElement === panel ||
        document.activeElement === (event.shiftKey ? first : last)) {
        event.preventDefault()
        const nextFocus = (event.shiftKey ? last : first) ?? panel
        nextFocus.focus()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, onClose, dismissible])

  if (!open) {
    return null
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[80] flex items-start justify-center bg-black/40 p-4 pt-[10vh]"
      role="presentation"
      onMouseDown={(event) => {
        if (dismissible && event.target === event.currentTarget) {
          onClose()
        }
      }}
    >
      <div
        ref={panelRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        aria-describedby={descriptionId}
        className={cn('bg-background max-h-full w-full max-w-lg overflow-y-auto rounded-2xl border p-4 shadow-2xl outline-none', className)}
      >
        <header className="mb-3 flex items-center justify-between gap-3">
          <h2 className="text-base font-semibold">{title}</h2>
          <Button variant="ghost" size="icon" data-modal-close aria-label="关闭弹框" onClick={onClose} disabled={!dismissible}>
            <X aria-hidden="true" />
          </Button>
        </header>
        {children}
      </div>
    </div>,
    document.body,
  )
}
