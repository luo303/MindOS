import { X } from 'lucide-react'
import { useEffect, useRef } from 'react'

import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

type Props = {
  message: string
  onClose: () => void
  tone?: 'default' | 'error'
  duration?: number
  className?: string
}

/** 顶部轻提示，自动消失；用于导入/补齐等一次性反馈。 */
export function Toast({ message, onClose, tone = 'default', duration = 4200, className }: Props) {
  const closeRef = useRef(onClose)

  useEffect(() => {
    closeRef.current = onClose
  }, [onClose])

  useEffect(() => {
    const timer = window.setTimeout(() => closeRef.current(), duration)
    return () => window.clearTimeout(timer)
  }, [message, duration])

  return (
    <div
      role={tone === 'error' ? 'alert' : 'status'}
      className={cn(
        'bg-background fixed top-4 left-1/2 z-[90] flex max-w-[min(92vw,32rem)] -translate-x-1/2 items-start gap-3 rounded-xl border px-4 py-3 text-sm shadow-xl',
        tone === 'error' && 'border-destructive/40',
        className,
      )}
    >
      <p className="min-w-0 flex-1 break-words">{message}</p>
      <Button variant="ghost" size="icon" className="-mr-1 -mt-1 size-6 shrink-0" aria-label="关闭提示" onClick={onClose}>
        <X className="size-3.5" aria-hidden="true" />
      </Button>
    </div>
  )
}
