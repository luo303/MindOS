import { Check, ChevronDown } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

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

type MenuPosition = {
  top: number
  left?: number
  right?: number
  minWidth: number
}

/**
 * 统一的自绘下拉。
 *
 * 菜单通过 portal 挂到 body，避免被 glass-surface / transform / overflow
 * 形成的 stacking context 压到别的卡片下面；外层容器再也不能裁剪它。
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
  const [position, setPosition] = useState<MenuPosition | null>(null)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const listboxId = useId()
  const selected = options.find((option) => option.value === value)

  useEffect(() => {
    if (!open) {
      return
    }
    const place = () => {
      const rect = triggerRef.current?.getBoundingClientRect()
      if (!rect) {
        return
      }
      setPosition({
        top: rect.bottom + 8,
        minWidth: Math.max(rect.width, 144),
        ...(align === 'right'
          ? { right: Math.max(8, window.innerWidth - rect.right) }
          : { left: Math.max(8, rect.left) }),
      })
    }
    place()

    const onPointerDown = (event: MouseEvent) => {
      const target = event.target as Node
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) {
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
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      window.removeEventListener('mousedown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [open, align])

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        ref={triggerRef}
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

      {open && position
        ? createPortal(
            <div
              ref={menuRef}
              id={listboxId}
              role="listbox"
              aria-label={ariaLabel + '选项'}
              className={cn(
                'bg-background fixed z-[100] max-h-72 overflow-y-auto rounded-xl border p-1 shadow-xl',
                menuClassName ?? 'w-56',
              )}
              style={{
                top: position.top,
                left: position.left,
                right: position.right,
                minWidth: position.minWidth,
              }}
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
            </div>,
            document.body,
          )
        : null}
    </div>
  )
}
