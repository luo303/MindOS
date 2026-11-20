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
    <main className="flex min-h-full items-center justify-center px-6 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center gap-2">
          <img src="/favicon.svg" alt="" className="size-6 rounded-md" />
          <span className="font-semibold tracking-tight">MindOS</span>
        </div>

        <h1 className="border-b pb-4 text-[1.75rem] font-semibold tracking-tight">{title}</h1>

        <div className="mt-6">{children}</div>
        <div className="text-muted-foreground mt-6 text-sm">{footer}</div>
      </div>
    </main>
  )
}
