import { useQuery } from '@tanstack/react-query'

import { describeError } from '@/api/errors'
import { fetchMemoryCandidates, fetchPreferences, memoryKeys } from '@/api/memory'
import { ErrorState } from '@/components/error-state'
import { PageHeader } from '@/components/page-header'
import { Skeleton } from '@/components/ui/skeleton'

import { MemoryCandidateCard } from './memory/candidate-card'
import { PreferenceCard } from './memory/preference-card'
import { preferenceKeys } from './memory/preferences'

export function MemoryPage() {
  const saved = useQuery({ queryKey: memoryKeys.preferences, queryFn: ({ signal }) => fetchPreferences(signal) })
  const candidates = useQuery({ queryKey: memoryKeys.candidates, queryFn: ({ signal }) => fetchMemoryCandidates(signal), refetchInterval: 5_000 })

  return (
    <div className="mx-auto w-full max-w-4xl space-y-6 px-4 py-5 sm:px-6">
      <div>
        <PageHeader title="记忆" />
        <p className="text-muted-foreground text-sm">管理 Lore 跨会话使用的默认偏好。本轮明确要求优先。</p>
      </div>
      <section aria-labelledby="memory-preferences" className="space-y-3">
        <h2 id="memory-preferences" className="text-lg font-semibold">已保存的偏好</h2>
        {saved.isPending ? <Skeleton className="h-24 w-full" /> : null}
        {saved.isError ? <ErrorState title="加载偏好失败" description={describeError(saved.error)} onRetry={() => void saved.refetch()} retrying={saved.isFetching} /> : null}
        {saved.isSuccess ? preferenceKeys.map((key) => <PreferenceCard key={key} preferenceKey={key} item={saved.data.find((item) => item.key === key)} />) : null}
      </section>
      <section aria-labelledby="memory-inbox" className="space-y-3">
        <h2 id="memory-inbox" className="text-lg font-semibold">待确认</h2>
        {candidates.isPending ? <Skeleton className="h-24 w-full" /> : null}
        {candidates.isError ? <ErrorState title="加载记忆建议失败" description={describeError(candidates.error)} onRetry={() => void candidates.refetch()} retrying={candidates.isFetching} /> : null}
        {candidates.data?.length === 0 ? <p className="text-muted-foreground text-sm">暂无建议。可以在对话中说「记住，以后代码示例默认用 Go」。</p> : null}
        {candidates.data?.map((item) => (
          <MemoryCandidateCard key={item.id} item={item} currentValue={saved.data?.find((current) => current.key === item.key && current.status === 'active')?.value} />
        ))}
      </section>
    </div>
  )
}
