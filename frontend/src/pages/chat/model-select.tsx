import { Check, ChevronDown } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

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
 * 用自建 popover 而不是原生 select：原生 option 无法跟随玻璃拟态样式，
 * 这里只需要一个轻量列表，键盘 Escape/点击外部关闭即可。
 */
export function ModelSelect({ models, value, onChange, disabled }: Props) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const selected = models.find((model) => model.id === value) ?? models[0]

  useEffect(() => {
    if (!open) {
      return
    }
    const onPointerDown = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) {
        setOpen(false)
      }
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }
    window.addEventListener('mousedown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('mousedown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  if (models.length === 0) {
    return null
  }

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        type="button"
        className="glass-surface text-muted-foreground hover:text-foreground flex h-8 max-w-44 items-center gap-1.5 rounded-full border px-3 text-xs transition-colors disabled:opacity-50 sm:max-w-56"
        aria-label="选择对话模型"
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((current) => !current)}
      >
        <span className="min-w-0 truncate">{selected?.name ?? '选择模型'}</span>
        <ChevronDown className="size-3.5 shrink-0" aria-hidden="true" />
      </button>

      {open ? (
        <div
          role="listbox"
          aria-label="对话模型列表"
          className="bg-background absolute top-full right-0 z-50 mt-2 w-64 rounded-xl border p-1 shadow-xl"
        >
          {models.map((model) => {
            const active = model.id === selected?.id
            return (
              <button
                key={model.id}
                type="button"
                role="option"
                aria-selected={active}
                className={
                  'hover:bg-muted flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left transition-colors ' +
                  (active ? 'bg-muted/70' : '')
                }
                onClick={() => {
                  onChange(model.id)
                  setOpen(false)
                }}
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{model.name}</span>
                  <span className="text-muted-foreground block text-[11px]">{model.model}</span>
                </span>
                {active ? <Check className="text-primary size-4 shrink-0" aria-hidden="true" /> : null}
              </button>
            )
          })}
        </div>
      ) : null}
    </div>
  )
}
