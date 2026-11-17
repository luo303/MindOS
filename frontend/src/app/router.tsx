import { createBrowserRouter, type RouteObject } from 'react-router'

import { AppShell } from '@/app/AppShell'
import { RequireAuth } from '@/app/require-auth'
import { ChatPage } from '@/pages/chat'
import { DashboardPage } from '@/pages/dashboard'
import { KnowledgePage } from '@/pages/knowledge'
import { LoginPage } from '@/pages/login'
import { MemoryPage } from '@/pages/memory'
import { NotFoundPage } from '@/pages/not-found'
import { RegisterPage } from '@/pages/register'
import { SettingsPage } from '@/pages/settings'
import { TasksPage } from '@/pages/tasks'

/**
 * 路由表单独导出：生产用 createBrowserRouter，测试用 createMemoryRouter，
 * 两者共享同一份 routes，测试覆盖的就是线上真实配置。
 *
 * 登录与注册在守卫外面：它们本来就是为了拿到登录态。其余全部挂在 RequireAuth
 * 之下——新增页面不需要记得"再加一次校验"，这是默认安全的那一侧。
 */
export const routes: RouteObject[] = [
  { path: '/login', element: <LoginPage /> },
  { path: '/register', element: <RegisterPage /> },
  {
    path: '/',
    element: (
      <RequireAuth>
        <AppShell />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <DashboardPage /> },
      { path: 'chat', element: <ChatPage /> },
      { path: 'knowledge', element: <KnowledgePage /> },
      { path: 'memory', element: <MemoryPage /> },
      { path: 'tasks', element: <TasksPage /> },
      { path: 'settings', element: <SettingsPage /> },
      // 通配放在最内层：保留导航外壳，用户还能直接点回别的页面
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]

export const router = createBrowserRouter(routes)
