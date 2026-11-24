import { ArrowUp, Loader2, Plus } from 'lucide-react'
import { useRef } from 'react'

import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

type ChatComposerProps = {
  onSubmit: (value: string) => void
  pending: boolean
  disabled: boolean
}

/** 一行起步、最多约 6 行；超过后 textarea 内部滚动。 */
export function ChatComposer({ onSubmit, pending, disabled }: ChatComposerProps) {
  const ref = useRef<HTMLTextAreaElement | null>(null)

  const send = () => {
    const value = ref.current?.value ?? ''
    if (value.trim() === '') {
      return
    }
    onSubmit(value)
    if (ref.current) {
      ref.current.value = ''
      ref.current.style.height = ''
    }
  }

  return (
    <form
      aria-label="发送消息"
      className="glass-surface space-y-2 rounded-3xl border p-3 transition-[border-color,box-shadow] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/20"
      onSubmit={(event) => {
        event.preventDefault()
        send()
      }}
    >
      <Textarea
        ref={ref}
        placeholder="给 MindOS 发送消息"
        rows={1}
        maxLength={500}
        disabled={disabled}
        aria-label="输入问题"
        aria-keyshortcuts="Meta+Enter Control+Enter"
        onInput={(event) => {
          // 兼容不支持 field-sizing:content 的浏览器；jsdom 的 scrollHeight=0 时跳过。
          const el = event.currentTarget
          if (el.scrollHeight === 0) {
            return
          }
          el.style.height = 'auto'
          el.style.height = Math.min(el.scrollHeight, 160) + 'px'
          el.style.overflowY = el.scrollHeight > 160 ? 'auto' : 'hidden'
        }}
        onKeyDown={(event) => {
          if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
            event.preventDefault()
            send()
          }
        }}
        className="placeholder:text-muted-foreground max-h-40 min-h-10 resize-none overflow-y-auto border-0 bg-transparent px-1 py-2 text-sm leading-6 shadow-none outline-none focus-visible:border-transparent focus-visible:ring-0 [field-sizing:content] disabled:cursor-not-allowed disabled:opacity-50"
      />

      <div className="flex items-center justify-between">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="rounded-full"
          aria-label="添加附件，暂未开放"
          title="暂未开放"
        >
          <Plus aria-hidden="true" />
        </Button>

        <Button
          type="button"
          size="icon"
          className="rounded-full"
          disabled={disabled || pending}
          aria-label={pending ? '生成中' : '发送'}
          onClick={send}
        >
          {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <ArrowUp aria-hidden="true" />}
        </Button>
      </div>
    </form>
  )
}

/**
 * 把后端裁决翻成界面阶段。
 */
