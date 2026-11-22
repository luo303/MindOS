import { ArrowRight, ArrowUpRight } from 'lucide-react'
import { Link } from 'react-router'

import { getModule } from '@/app/modules'
import { Card } from '@/components/ui/card'
import { RecentDocuments, OpenTodos } from '@/pages/dashboard/activity'

const shortcuts = [getModule('chat'), getModule('knowledge'), getModule('tasks')]

export function DashboardPage() {
  return (
    <div className="mx-auto w-full max-w-6xl space-y-4 sm:space-y-5">
      <header className="desk-hero">
        <div className="space-y-5">
          <h1>工作台</h1>
          <Link to="/chat" className="desk-hero-action">
            打开对话 <ArrowRight className="size-4" aria-hidden="true" />
          </Link>
        </div>
      </header>

      <nav aria-label="快捷入口">
        <ul className="grid grid-cols-1 gap-2 min-[390px]:grid-cols-3 sm:gap-4">
          {shortcuts.map(({ key, path, label, icon: Icon }) => (
            <li key={key} className="min-w-0">
              <Card interactive className="p-0">
                <Link to={path} className="desk-shortcut group">
                  <span className="desk-shortcut-icon"><Icon size={19} strokeWidth={1.7} aria-hidden="true" /></span>
                  <span>{label}</span>
                  <ArrowUpRight className="text-muted-foreground ml-auto size-4 shrink-0 group-hover:text-primary" aria-hidden="true" />
                </Link>
              </Card>
            </li>
          ))}
        </ul>
      </nav>

      <div className="grid gap-4 sm:grid-cols-[1.35fr_1fr]">
        <RecentDocuments />
        <OpenTodos />
      </div>
    </div>
  )
}
