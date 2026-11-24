import type { InputHTMLAttributes, MouseEvent } from 'react'

import { cn } from '@/lib/utils'

const pickerTypes = new Set(['date', 'datetime-local', 'month', 'time', 'week'])

/** 文本框；日期类型点击整框打开浏览器原生选择器。 */
export function Input({ className, type = 'text', onClick, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  const isPicker = pickerTypes.has(type)

  function handlePickerClick(event: MouseEvent<HTMLInputElement>) {
    onClick?.(event)
    const input = event.currentTarget
    if (event.defaultPrevented || input.disabled || input.readOnly) {
      return
    }
    try {
      input.showPicker?.()
    } catch {
      return
    }
  }

  return (
    <input
      data-slot="input"
      type={type}
      onClick={isPicker ? handlePickerClick : onClick}
      className={cn(
        'border-input bg-background flex h-9 w-full min-w-0 rounded-md border px-3 py-1 text-sm shadow-xs transition-[color,box-shadow] outline-none',
        'placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
        'disabled:cursor-not-allowed disabled:opacity-50',
        isPicker && 'cursor-pointer read-only:cursor-default',
        className,
      )}
      {...props}
    />
  )
}
