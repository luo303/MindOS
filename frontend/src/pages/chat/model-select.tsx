import type { ChatModel } from '@/api/types'
import { Select } from '@/components/ui/select'

type Props = {
  models: ChatModel[]
  value: number | null
  onChange: (id: number) => void
  disabled?: boolean
}

/** 模型下拉只是通用 Select 的一层数据适配。 */
export function ModelSelect({ models, value, onChange, disabled }: Props) {
  if (models.length === 0) {
    return null
  }
  const selected = models.find((model) => model.id === value) ?? models[0]
  return (
    <Select
      value={String(selected.id)}
      options={models.map((model) => ({
        value: String(model.id),
        label: model.name,
        description: model.model,
      }))}
      onChange={(next) => onChange(Number(next))}
      ariaLabel="选择对话模型"
      disabled={disabled}
      menuClassName="w-64"
    />
  )
}
