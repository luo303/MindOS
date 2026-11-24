import { ArrowDown } from 'lucide-react'
import { useLayoutEffect, useRef, useState, type ReactNode, type UIEvent } from 'react'

import { Button } from '@/components/ui/button'

const FOLLOW_DISTANCE = 24
const JUMP_BUTTON_DISTANCE = 160

function bottomOffset(viewport: HTMLDivElement): number {
  return Math.max(0, viewport.scrollHeight - viewport.clientHeight)
}

type Props = {
  streaming: boolean
  children: ReactNode
}

export function MessageViewport({ streaming, children }: Props) {
  const viewportRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const followsLatest = useRef(true)
  const [showJump, setShowJump] = useState(false)

  useLayoutEffect(() => {
    const viewport = viewportRef.current
    const content = contentRef.current
    if (!viewport || !content) {
      return
    }
    viewport.scrollTop = bottomOffset(viewport)
    if (typeof ResizeObserver === 'undefined') {
      return
    }
    const observer = new ResizeObserver(() => {
      const bottom = bottomOffset(viewport)
      if (followsLatest.current) {
        viewport.scrollTop = bottom
      }
      setShowJump(bottom - viewport.scrollTop > JUMP_BUTTON_DISTANCE)
    })
    observer.observe(viewport)
    observer.observe(content)
    return () => observer.disconnect()
  }, [])

  function handleScroll(event: UIEvent<HTMLDivElement>) {
    const viewport = event.currentTarget
    const distance = bottomOffset(viewport) - viewport.scrollTop
    followsLatest.current = distance <= FOLLOW_DISTANCE
    setShowJump(distance > JUMP_BUTTON_DISTANCE)
  }

  function handleJump() {
    const viewport = viewportRef.current
    if (!viewport) {
      return
    }
    const animate = !streaming && !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    const bottom = bottomOffset(viewport)
    viewport.focus({ preventScroll: true })
    if (animate && typeof viewport.scrollTo === 'function') {
      viewport.scrollTo({ top: bottom, behavior: 'smooth' })
      return
    }
    followsLatest.current = true
    viewport.scrollTop = bottom
    setShowJump(false)
  }

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={viewportRef}
        role="region"
        aria-label="会话消息"
        tabIndex={0}
        onScroll={handleScroll}
        className="chat-scroll min-h-0 flex-1 overflow-y-auto"
      >
        <div ref={contentRef}>{children}</div>
      </div>
      {showJump ? (
        <Button
          variant="outline"
          size="icon"
          aria-label="回到底部"
          title="回到底部"
          onClick={handleJump}
          className="bg-background absolute bottom-4 left-1/2 z-10 size-9 -translate-x-1/2 rounded-full shadow-md sm:size-10"
        >
          <ArrowDown aria-hidden="true" />
        </Button>
      ) : null}
    </div>
  )
}
