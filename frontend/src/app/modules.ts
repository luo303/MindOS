import {
  BookOpen,
  Brain,
  LayoutDashboard,
  ListChecks,
  MessageSquare,
  Settings,
  type LucideIcon,
} from 'lucide-react'

/**
 * 导航项也供工作台入口和未接入页复用，避免同一入口有多套名称。
 */
export type ModuleKey = 'dashboard' | 'chat' | 'knowledge' | 'memory' | 'tasks' | 'settings'

export type ModuleDef = {
  key: ModuleKey
  path: string
  label: string
  icon: LucideIcon
}

export const modules: readonly ModuleDef[] = [
  {
    key: 'dashboard',
    path: '/',
    label: '工作台',
    icon: LayoutDashboard,
  },
  {
    key: 'chat',
    path: '/chat',
    label: '对话',
    icon: MessageSquare,
  },
  {
    key: 'knowledge',
    path: '/knowledge',
    label: '知识',
    icon: BookOpen,
  },
  {
    key: 'memory',
    path: '/memory',
    label: '记忆',
    icon: Brain,
  },
  {
    key: 'tasks',
    path: '/tasks',
    label: '任务',
    icon: ListChecks,
  },
  {
    key: 'settings',
    path: '/settings',
    label: '设置',
    icon: Settings,
  },
]

export function getModule(key: ModuleKey): ModuleDef {
  const found = modules.find((item) => item.key === key)

  // modules 是常量表，取不到只可能是 key 写错，属于开发期错误，
  // 早抛比渲染出一个空白页面更容易发现。
  if (!found) {
    throw new Error(`unknown module key: ${key}`)
  }
  return found
}
