import { Link } from 'react-router'

import { PageHeader } from '@/components/page-header'

export function NotFoundPage() {
  return (
    <div className="mx-auto w-full max-w-5xl">
      <PageHeader title="页面不存在" />
      <Link className="text-primary mt-4 inline-flex py-2 text-sm font-medium underline underline-offset-4" to="/">
        返回工作台
      </Link>
    </div>
  )
}
