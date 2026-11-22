import { ChevronDown, LogOut } from 'lucide-react'
import { Link, Outlet, useLocation } from 'react-router'

import { useLogout, useMe } from '@/app/auth'
import { WorkspaceNav } from '@/app/workspace-nav'
import { describeError } from '@/api/errors'
import { Button } from '@/components/ui/button'

export function AppShell() {
  const { pathname } = useLocation()
  const fixedChat = pathname.startsWith('/chat')

  return (
    <div className="workspace-canvas flex h-full min-w-0 flex-col">
      <a href="#workspace" className="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:rounded-md focus:bg-background focus:p-3">
        跳到主内容
      </a>
      <header className="workspace-bar">
        <div className="workspace-bar-inner">
          <Link to="/" className="brand-wordmark">
            <img src="/favicon.svg" alt="" className="size-8" />
            MindOS
          </Link>
          <WorkspaceNav />
          <AccountPanel />
        </div>
      </header>

      <main
        id="workspace"
        tabIndex={-1}
        className={fixedChat ? 'workspace-main workspace-main-chat' : 'workspace-main'}
      >
        <Outlet />
      </main>
    </div>
  )
}

function AccountPanel() {
  const { data } = useMe()
  const logout = useLogout()
  const name = data?.display_name || data?.email || '账号'

  return (
    <details
      className="account-menu"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) event.currentTarget.open = false
      }}
      onKeyDown={(event) => {
        if (event.key !== 'Escape' || !event.currentTarget.open) return
        event.currentTarget.open = false
        event.currentTarget.querySelector('summary')?.focus()
      }}
    >
      <summary className="account-trigger" aria-label="账号">
        <span className="account-avatar" aria-hidden="true">{Array.from(name)[0]?.toUpperCase()}</span>
        <span className="hidden min-w-0 max-w-28 truncate text-sm font-medium lg:block">{name}</span>
        <ChevronDown className="text-muted-foreground size-3.5 shrink-0" aria-hidden="true" />
      </summary>
      <div className="account-popover">
        <div className="px-3 pt-2 pb-3">
          <p className="text-sm font-semibold break-words">{name}</p>
          {name !== data?.email ? <p className="text-muted-foreground mt-0.5 text-xs break-all">{data?.email}</p> : null}
        </div>
        <Button variant="ghost" size="sm" className="w-full justify-start" disabled={logout.isPending} onClick={() => logout.mutate()}>
          <LogOut aria-hidden="true" />
          {logout.isPending ? '退出中…' : '退出登录'}
        </Button>
        {logout.error ? <p role="alert" className="text-destructive px-3 py-2 text-xs">{describeError(logout.error)}</p> : null}
      </div>
    </details>
  )
}
