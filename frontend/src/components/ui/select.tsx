import { Check, ChevronDown } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'

import { cn } from '@/lib/utils'

export type SelectOption = {
  value: string
  label: string
  description?: string
  disabled?: boolean
}

type Props = {
  value: string
  options: SelectOption[]
  onChange: (value: string) => void
  ariaLabel: string
  placeholder?: string
  disabled?: boolean
  className?: string
  menuClassName?: string
  align?: 'left' | 'right'
}

/**
 * 统一的自绘下拉。
 *
 * 原生 select 的 option 无法跟随玻璃拟态和暗色主题，这里用一个轻量
 * listbox 替代；点击外部/按 Escape 关闭，当前项带勾选。
 */
export function Select({
  value,
  options,
  onChange,
  ariaLabel,
  placeholder = '请选择',
  disabled = false,
  className,
  menuClassName,
  align = 'right',
}: Props) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const listboxId = useId()
  const selected = options.find((option) => option.value === value)

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

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        type="button"
        className={cn(
          'glass-surface text-muted-foreground hover:text-foreground flex h-8 max-w-56 items-center gap-1.5 rounded-full border px-3 text-xs transition-colors disabled:opacity-50',
          className,
        )}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxId : undefined}
        disabled={disabled}
        onClick={() => setOpen((current) => !current)}
      >
        <span className="min-w-0 truncate">{selected?.label ?? placeholder}</span>
        <ChevronDown className="size-3.5 shrink-0" aria-hidden="true" />
      </button>

      {open ? (
        <div
          id={listboxId}
          role="listbox"
          aria-label={ariaLabel + '选项'}
          className={cn(
            'bg-background absolute top-full z-50 mt-2 min-w-full rounded-xl border p-1 shadow-xl',
            align === 'right' ? 'right-0' : 'left-0',
            menuClassName ?? 'w-56',
          )}
        >
          {options.map((option) => {
            const active = option.value === value
            return (
              <button
                key={option.value}
                type="button"
                role="option"
                aria-selected={active}
                disabled={option.disabled}
                className={cn(
                  'hover:bg-muted flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left transition-colors disabled:opacity-40',
                  active && 'bg-muted/70',
                )}
                onClick={() => {
                  onChange(option.value)
                  setOpen(false)
                }}
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{option.label}</span>
                  {option.description ? (
                    <span className="text-muted-foreground block truncate text-[11px]">{option.description}</span>
                  ) : null}
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
