import { getModule, type ModuleKey } from '@/app/modules'
import { EmptyState } from '@/components/empty-state'
import { PageHeader } from '@/components/page-header'

/**
 * 尚未接入的模块不展示模拟内容，也不承诺上线时间。
 */
export function ModulePlaceholder({ moduleKey }: { moduleKey: ModuleKey }) {
  const item = getModule(moduleKey)

  return (
    <div className="mx-auto w-full max-w-5xl">
      <PageHeader title={item.label} />
      <EmptyState title="尚未接入" />
    </div>
  )
}
