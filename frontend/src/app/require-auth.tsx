import { useQueryClient } from '@tanstack/react-query'
import { useEffect, type ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router'

import { authQueryKeys } from '@/api/auth'
import { describeError } from '@/api/errors'
import { isUnauthorized, loginPathFor, useMe } from '@/app/auth'
import { ErrorState } from '@/components/error-state'
import { Skeleton } from '@/components/ui/skeleton'

/**
 * 路由守卫：未登录跳登录页，并记住原地址。
 *
 * 顺序很重要：**先看 data 再看 error**。后台刷新失败时 React Query 会保留旧
 * data，此时应该继续渲染已登录界面；反过来先看 error 会把一个可用的页面
 * 闪成错误页。
 *
 * 新增页面挂在守卫之下就不需要再记得"加一次校验"——默认是安全的那一侧。
 */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { data, error, refetch } = useMe()
  const location = useLocation()
  const queryClient = useQueryClient()

  useUnauthorizedListener(queryClient)

  if (data) {
    return <>{children}</>
  }
  if (error) {
    if (isUnauthorized(error)) {
      return <Navigate to={loginPathFor(location)} replace />
    }
    return (
      <div className="p-6">
        <ErrorState
          title="无法确认登录状态"
          description={describeError(error)}
          onRetry={() => void refetch()}
        />
      </div>
    )
  }
  if (data === null) {
    // 测试会预置 null 表示"未登录"；真实请求失败不会走到这里（会先有 error）。
    return <Navigate to={loginPathFor(location)} replace />
  }
  return <FullPageSkeleton />
}

/**
 * 监听全局的 401 广播（由 api 客户端在收到 401 时发出）。
 *
 * 只在"曾经登录过"时清理缓存：否则一次 401 会触发重新查询 → 再 401 的循环。
 */
function useUnauthorizedListener(queryClient: ReturnType<typeof useQueryClient>) {
  useEffect(() => {
    const onUnauthorized = () => {
      if (queryClient.getQueryData(authQueryKeys.me)) {
        queryClient.removeQueries({ queryKey: authQueryKeys.me })
      }
    }
    window.addEventListener('mindos:unauthorized', onUnauthorized)
    return () => window.removeEventListener('mindos:unauthorized', onUnauthorized)
  }, [queryClient])
}

function FullPageSkeleton() {
  return (
    <div className="mx-auto w-full max-w-5xl space-y-4 p-6" aria-label="加载中">
      <Skeleton className="h-8 w-1/3" />
      <Skeleton className="h-40 w-full" />
    </div>
  )
}
