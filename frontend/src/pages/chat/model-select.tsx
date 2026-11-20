import type { ChatModel } from '@/api/types'

type Props = {
  models: ChatModel[]
  value: number | null
  onChange: (id: number) => void
  disabled?: boolean
}

/**
 * 对话模型选择器。
 *
 * 用原生 select 而不是自建下拉：项目里还没有 Select 组件，
 * 为了一个下拉引入一整套 popover/键盘导航不划算；原生 select 在
 * 键盘操作与移动端上反而更可靠。
 */
export function ModelSelect({ models, value, onChange, disabled }: Props) {
  if (models.length === 0) {
    return null
  }
  return (
    <select
      className="border-input bg-background text-muted-foreground h-8 max-w-full min-w-0 rounded-md border px-2 text-xs disabled:opacity-50 sm:max-w-64"
      value={value ?? ''}
      onChange={(event) => onChange(Number(event.target.value))}
      disabled={disabled}
      aria-label="选择对话模型"
    >
      {models.map((model) => (
        <option key={model.id} value={model.id}>
          {model.name}（{model.model}）
        </option>
      ))}
    </select>
  )
}
