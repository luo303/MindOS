import type { ReactNode } from 'react'

/**
 * 登录/注册页的公共外壳。
 *
 * 它与 AppShell 的区别是"不假设已登录"：没有侧栏、没有当前用户，
 * 只有一个居中的表单。放在 AppShell 里会让未登录用户看到一套点不动的导航。
 */
export function AuthShell({
  title,
  children,
  footer,
}: {
  title: string
  children: ReactNode
  footer: ReactNode
}) {
  return (
    <main className="workspace-canvas flex min-h-full items-center justify-center px-4 py-8 sm:px-6">
      <div className="glass-surface w-full max-w-[26rem] px-6 py-7 sm:px-9 sm:py-9">
        <div className="brand-wordmark mb-7">
          <img src="/favicon.svg" alt="" className="size-9" />
          MindOS
        </div>
        <h1 className="text-[1.9rem] font-semibold tracking-tight">{title}</h1>

        <div className="mt-6">{children}</div>
        <div className="text-muted-foreground mt-6 text-sm">{footer}</div>
      </div>
    </main>
  )
}
