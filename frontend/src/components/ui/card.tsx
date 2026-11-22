import type { HTMLAttributes, PointerEvent } from 'react'

import { cn } from '@/lib/utils'

type CardProps = HTMLAttributes<HTMLDivElement> & { interactive?: boolean }

function followPointer(event: PointerEvent<HTMLDivElement>): void {
  if (event.pointerType !== 'mouse' || !window.matchMedia('(hover: hover) and (pointer: fine) and (prefers-reduced-motion: no-preference)').matches) return
  const card = event.currentTarget
  const rect = card.getBoundingClientRect()
  if (!rect.width || !rect.height) return
  const x = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width))
  const y = Math.min(1, Math.max(0, (event.clientY - rect.top) / rect.height))
  card.style.setProperty('--glass-x', `${x * 100}%`)
  card.style.setProperty('--glass-y', `${y * 100}%`)
  card.style.setProperty('--glass-rx', `${(0.5 - y) * 5}deg`)
  card.style.setProperty('--glass-ry', `${(x - 0.5) * 5}deg`)
  card.dataset.lit = ''
}

export function Card({ className, interactive = false, children, ...props }: CardProps) {
  const surface = (
    <div data-slot="card" className={cn('glass-surface text-card-foreground flex min-w-0 flex-col gap-4 py-5', className)} {...props}>
      {children}
    </div>
  )
  if (!interactive) return surface

  return (
    <div
      className="glass-card"
      onPointerMove={followPointer}
      onPointerLeave={(event) => { delete event.currentTarget.dataset.lit }}
      onPointerCancel={(event) => { delete event.currentTarget.dataset.lit }}
    >
      {surface}
    </div>
  )
}

export function CardHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div data-slot="card-header" className={cn('flex flex-col gap-1 px-5', className)} {...props} />
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return <h3 data-slot="card-title" className={cn('font-semibold leading-none', className)} {...props} />
}

export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p data-slot="card-description" className={cn('text-muted-foreground text-sm', className)} {...props} />
  )
}

export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div data-slot="card-content" className={cn('px-5', className)} {...props} />
}

export function CardFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div data-slot="card-footer" className={cn('flex items-center px-5', className)} {...props} />
}
