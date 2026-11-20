import { useEffect, useRef, useState } from 'react'

/**
 * 把"按 chunk 到达的文本"平滑成打字机效果。
 *
 * 模型/SSE 的 delta 往往一次包含几个词甚至一整句，直接 append 会一跳一段。
 * 这里保留完整目标文本，另用一个逐帧追赶的显示文本：
 * - 正常积压：约 30 字/秒；
 * - 积压较多：自动加速，避免流结束后还在慢慢补；
 * - 流结束/失败：立即显示完整文本。
 */
const FRAME_MS = 33 // 约 30fps，比 60fps 少一半重渲染
const STEP_NORMAL = 1
const STEP_FAST = 3
const STEP_CATCH_UP = 6
const BACKLOG_FAST = 80
const BACKLOG_CATCH_UP = 160

export function useTypewriter(target: string, active: boolean): string {
  const [shown, setShown] = useState(active ? '' : target)
  const shownRef = useRef(shown)
  const targetRef = useRef(target)
  const lastFrameRef = useRef(0)
  const rafRef = useRef<number | null>(null)

  useEffect(() => {
    targetRef.current = target
  }, [target])

  useEffect(() => {
    if (!active) {
      shownRef.current = targetRef.current
      setShown(targetRef.current)
      return
    }

    // 新一轮/重试：目标比已显示短，说明文本被重置了。
    if (targetRef.current.length < shownRef.current.length) {
      shownRef.current = ''
      setShown('')
    }

    const tick = (now: number) => {
      rafRef.current = requestAnimationFrame(tick)
      if (now - lastFrameRef.current < FRAME_MS) {
        return
      }
      lastFrameRef.current = now

      const full = targetRef.current
      const current = shownRef.current
      if (current.length >= full.length) {
        return
      }
      const backlog = full.length - current.length
      const step =
        backlog > BACKLOG_CATCH_UP
          ? STEP_CATCH_UP
          : backlog > BACKLOG_FAST
            ? STEP_FAST
            : STEP_NORMAL
      const next = full.slice(0, current.length + step)
      shownRef.current = next
      setShown(next)
    }

    rafRef.current = requestAnimationFrame(tick)
    return () => {
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
    }
  }, [active])

  return shown
}
