import { ArrowRight } from 'lucide-react'
import { Link } from 'react-router'

import { getModule } from '@/app/modules'
import { PageHeader } from '@/components/page-header'

const shortcuts = [getModule('chat'), getModule('knowledge')]

export function DashboardPage() {
  return (
    <div className="mx-auto w-full max-w-5xl">
      <PageHeader title={getModule('dashboard').label} />
      <nav aria-label="快捷入口" className="mt-4 max-w-lg">
        <ul className="divide-y">
          {shortcuts.map(({ key, path, label, icon: Icon }) => (
            <li key={key}>
              <Link to={path} className="hover:bg-accent flex items-center gap-4 rounded-md px-3 py-4 text-sm font-medium">
                <Icon className="text-primary size-5" aria-hidden="true" />
                {label}
                <ArrowRight className="text-muted-foreground ml-auto size-4" aria-hidden="true" />
              </Link>
            </li>
          ))}
        </ul>
      </nav>
    </div>
  )
}
