import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'

import {
  authQueryKeys,
  fetchMe,
  login,
  logout,
  register,
  type AuthUser,
  type LoginInput,
  type RegisterInput,
} from '@/api/auth'
import { ApiError } from '@/api/errors'

/**
 * 登录态的读写都收在这里：当前用户、登录、注册、登出。
 *
 * 前端不保存 token（Cookie 是 HttpOnly 的），所以"是否登录"只有一个判据：
 * /auth/me 能不能成功。没有 localStorage 里的第二份真相，也就不会出现
 * "界面以为登录着、请求全是 401"。
 *
 * 与 RequireAuth 分文件：那个文件导出的是组件，混着导出 hook 会让
 * react-refresh 的"只导出组件"规则失效（改一个 hook 就整页热更新失效）。
 */
export function useMe() {
  return useQuery<AuthUser | null>({
    queryKey: authQueryKeys.me,
    queryFn: ({ signal }) => fetchMe(signal),
    // 1 分钟：够久到不重复请求，又短到换个账号后不会长时间拿旧身份。
    staleTime: 60_000,
  })
}

/**
 * 清掉上一个账号留下的业务缓存。
 *
 * 退出/登录/注册时只改 auth.me 不够：会话、消息、文档这些 query key
 * 不带用户 id，新账号会先渲染旧账号的缓存（也会把旧数据展示给新账号）。
 * 保留 auth.me 自己，由调用方紧接着写入新值或 null。
 */
function clearUserScopedQueries(queryClient: QueryClient) {
  queryClient.removeQueries({
    predicate: (query) => query.queryKey[0] !== authQueryKeys.me[0],
  })
}

/** 登录成功后把用户写进缓存，守卫与侧栏立即生效，不用再请求一次 /auth/me。 */
export function useLogin() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: LoginInput) => login(input),
    onSuccess: (result) => {
      clearUserScopedQueries(queryClient)
      queryClient.setQueryData(authQueryKeys.me, result.user)
    },
  })
}

export function useRegister() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: RegisterInput) => register(input),
    onSuccess: (result) => {
      clearUserScopedQueries(queryClient)
      queryClient.setQueryData(authQueryKeys.me, result.user)
    },
  })
}

/**
 * 登出：把缓存置为 null，守卫随即跳登录页。
 *
 * 不在这里 navigate：登出按钮与"会话过期自动登出"应该走同一条路径，
 * 导航只由守卫负责。用 onSettled 而不是 onSuccess：请求失败时本地也必须
 * 退出登录态，否则界面会停在"看起来还登录着、其实 Cookie 已经没了"。
 */
export function useLogout() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: logout,
    onSettled: () => {
      clearUserScopedQueries(queryClient)
      queryClient.setQueryData(authQueryKeys.me, null)
    },
  })
}

export function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401
}

/**
 * 只接受站内路径作为登录后的跳转目标。
 *
 * 不校验的话 ?next=https://evil.example 会把用户送到站外——这是经典的
 * 开放重定向。以双斜杠开头的也要挡：浏览器把它当协议相对 URL。
 */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/') || next.startsWith('//')) {
    return '/'
  }
  return next
}

/** 未登录时要跳去的地址：把原路径放进 next，登录后能原路返回。 */
export function loginPathFor(location: { pathname: string; search: string }): string {
  const next = location.pathname + location.search
  return next === '/' ? '/login' : '/login?next=' + encodeURIComponent(next)
}
