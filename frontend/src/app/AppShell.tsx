import { Menu, X } from 'lucide-react'
import { useState } from 'react'
import { NavLink, Outlet } from 'react-router'

import { modules } from '@/app/modules'
import { useLogout, useMe } from '@/app/auth'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/**
 * 应用外壳：左侧导航 + 主工作区。
 *
 * 窄屏（< md）不挤压内容，而是把导航收进抽屉——六个模块的侧边栏在手机上
 * 会吃掉一半宽度，宁可多点一次菜单。抽屉的开关状态只属于布局，不进全局 store。
 */
export function AppShell() {
  const [drawerOpen, setDrawerOpen] = useState(false)
  const closeDrawer = () => setDrawerOpen(false)

  return (
    <div className="flex h-full">
      <a href="#workspace" className="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:rounded-md focus:bg-background focus:p-3">
        跳到主内容
      </a>
      <aside className="hidden shrink-0 md:block">
        <SideNav />
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="bg-sidebar flex h-14 shrink-0 items-center gap-2 border-b px-3 md:hidden">
          <Button variant="ghost" size="icon" aria-label="打开导航" onClick={() => setDrawerOpen(true)}>
            <Menu aria-hidden="true" />
          </Button>
          <span className="font-semibold tracking-tight">MindOS</span>
        </header>

        <main id="workspace" tabIndex={-1} className="min-w-0 flex-1 overflow-y-auto px-4 py-6 sm:px-7 md:px-9 md:py-9 xl:px-12">
          <Outlet />
        </main>
      </div>

      {drawerOpen ? (
        <div className="fixed inset-0 z-50 md:hidden" role="dialog" aria-modal="true" aria-label="导航抽屉">
          {/* 遮罩用按钮实现：点击即关闭，也天然可被键盘聚焦 */}
          <button
            type="button"
            aria-hidden="true"
            tabIndex={-1}
            className="absolute inset-0 bg-black/30 hover:bg-black/35"
            onClick={closeDrawer}
          />
          <div className="absolute inset-y-0 left-0 shadow-xl">
            <SideNav onNavigate={closeDrawer} />
            <Button
              variant="ghost"
              size="icon"
              aria-label="关闭导航"
              className="absolute top-2 right-2"
              onClick={closeDrawer}
            >
              <X aria-hidden="true" />
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  )
}

function SideNav({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <div className="bg-sidebar text-sidebar-foreground border-sidebar-border flex h-full w-52 flex-col border-r">
      <div className="flex h-16 shrink-0 items-center gap-2.5 px-5">
        <img src="/favicon.svg" alt="" className="size-6" />
        <span className="font-semibold tracking-tight">MindOS</span>
      </div>

      <nav aria-label="主导航" className="flex-1 space-y-0.5 overflow-y-auto px-3 py-2">
        {modules.map((item) => (
          <NavLink
            key={item.key}
            to={item.path}
            // 只有根路径需要 end：不加的话每个页面都会匹配到 "/" 并高亮工作台
            end={item.path === '/'}
            onClick={onNavigate}
            className={({ isActive }) =>
              cn(
                'flex min-h-9 items-center gap-2.5 rounded-md px-3 py-2 text-sm transition-colors',
                isActive
                  ? 'bg-sidebar-accent text-sidebar-accent-foreground font-medium'
                  : 'text-muted-foreground hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground',
              )
            }
          >
            <item.icon className="size-4 shrink-0" aria-hidden="true" />
            {item.label}
          </NavLink>
        ))}
      </nav>

      <AccountPanel />
    </div>
  )
}

/**
 * 侧栏底部的账号区。
 *
 * 退出登录不在这里导航：把缓存置为 null 后，路由守卫会跳登录页。
 * 这样"主动登出"与"会话过期被动登出"走的是同一条路径，不会有两套逻辑。
 */
function AccountPanel() {
  const { data } = useMe()
  const logoutMutation = useLogout()

  return (
    <div className="border-sidebar-border space-y-2 border-t px-5 py-4">
      <div className="min-w-0">
        <p className="truncate text-sm font-medium">{data?.display_name ?? '—'}</p>
        <p className="text-muted-foreground truncate text-xs">{data?.email ?? ''}</p>
      </div>
      <Button
        variant="ghost"
        size="sm"
        className="-ml-2.5"
        disabled={logoutMutation.isPending}
        onClick={() => logoutMutation.mutate()}
      >
        {logoutMutation.isPending ? '退出中…' : '退出登录'}
      </Button>
    </div>
  )
}
