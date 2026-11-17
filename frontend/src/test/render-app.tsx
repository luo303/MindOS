import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'

import { authQueryKeys, type AuthUser } from '@/api/auth'
import { routes } from '@/app/router'
import { authUserFixture } from '@/test/fixtures'

export type RenderAppOptions = {
  /**
   * 预置的登录态。省略表示"已登录"（用 fixture）；
   * 传 null 表示"未登录"，守卫会跳到登录页。
   */
  user?: AuthUser | null
}

/**
 * 按指定路径渲染整个应用（含真实路由表）。
 *
 * 测试里新建 QueryClient 而不是复用生产的单例：用例之间共享缓存会互相污染，
 * 例如上一个用例的成功结果会让下一个用例直接跳过请求。
 * retry 关掉，否则失败用例要等默认重试跑完才看得到错误态。
 *
 * 登录态是预置进缓存的：每条用例都先 mock 一次 /auth/me 只会让断言被握手细节淹没。
 * 这条查询的 staleTime 设为 Infinity，避免它在后台真的去请求接口。
 */
export function renderApp(initialPath = '/', options: RenderAppOptions = {}) {
  const user = options.user === undefined ? authUserFixture : options.user

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 0 } },
  })
  queryClient.setQueryDefaults(authQueryKeys.me, { staleTime: Infinity, retry: false })
  queryClient.setQueryData(authQueryKeys.me, user)

  const router = createMemoryRouter(routes, { initialEntries: [initialPath] })

  const utils = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )

  return { ...utils, queryClient, router }
}
