import type { HTMLAttributes } from 'react'

import { clearGlassPointer, followGlassPointer } from '@/lib/glass-pointer'
import { cn } from '@/lib/utils'

type CardProps = HTMLAttributes<HTMLDivElement> & { interactive?: boolean }

export function Card({ className, interactive = false, children, ...props }: CardProps) {
  const surface = (
    <div data-slot="card" className={cn('glass-surface text-card-foreground flex min-w-0 flex-col gap-4 py-5', interactive && 'glass-spotlight', className)} {...props}>
      {children}
    </div>
  )
  if (!interactive) return surface

  return (
    <div
      className="glass-card"
      onPointerMove={(event) => followGlassPointer(event, true)}
      onPointerLeave={clearGlassPointer}
      onPointerCancel={clearGlassPointer}
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
