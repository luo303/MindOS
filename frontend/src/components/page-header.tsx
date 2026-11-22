import type { ReactNode } from 'react'

type PageHeaderProps = {
  title: string
  actions?: ReactNode
}

export function PageHeader({ title, actions }: PageHeaderProps) {
  return (
    <header className="flex flex-wrap items-center justify-between gap-x-5 gap-y-3 pb-2 sm:pb-3">
      <h1 className="text-[1.9rem] leading-tight font-semibold tracking-tight sm:text-[2.5rem]">{title}</h1>
      {actions ? <div className="flex min-w-0 flex-wrap items-center gap-2">{actions}</div> : null}
    </header>
  )
}
