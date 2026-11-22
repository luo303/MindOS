import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react'

import { cn } from '@/lib/utils'

type PillPosition = { left: number; top: number; width: number; height: number }

type Props = {
  containerRef: RefObject<HTMLElement | null>
  active: unknown
  /** 当前项选择器，默认 aria-current="page"。 */
  selector?: string
  className?: string
  /** 列表内容变化时传一个新值，强制重新测量（新增/删除项时 active 可能不变）。 */
  revision?: string | number
}

/**
 * 滑动玻璃胶囊。顶部导航与会话列表共用：
 * - 切换到新项时，从旧位置滑过去；
 * - 移动中沿水平方向拉长，到位后回弹；
 * - 边缘用 inset 高光，和顶部导航保持同一套质感。
 */
export function SlidingPill({ containerRef, active, selector = '[aria-current="page"]', className, revision }: Props) {
  const pillRef = useRef<HTMLSpanElement | null>(null)

  useLayoutEffect(() => {
    const container = containerRef.current
    const pill = pillRef.current
    if (container && pill) {
      placePill(container, pill, selector, true)
      // 新条目可能在本帧稍后才完成布局；下一帧再测一次兜底。
      if (typeof window.requestAnimationFrame === 'function') {
        const frame = window.requestAnimationFrame(() => {
          placePill(container, pill, selector, false)
        })
        return () => window.cancelAnimationFrame(frame)
      }
    }
  }, [active, containerRef, selector, revision])

  // 首次挂载时子组件的 layout effect 可能早于父级 ref 就绪；补一次无动画定位。
  useEffect(() => {
    const container = containerRef.current
    const pill = pillRef.current
    if (container && pill && container.dataset.pill !== 'ready') {
      placePill(container, pill, selector, false)
    }
  }, [active, containerRef, selector, revision])

  useLayoutEffect(() => {
    const container = containerRef.current
    if (!container || typeof ResizeObserver === 'undefined') {
      return
    }
    const observer = new ResizeObserver(() => {
      const pill = pillRef.current
      if (pill) {
        placePill(container, pill, selector, false)
      }
    })
    observer.observe(container)
    container.querySelectorAll(selector).forEach((item) => observer.observe(item))

    // 列表新增/删除条目时也要重新定位。只观察首次挂载时的子项不够：
    // 新上传的文档刚插入、active 又没变时，胶囊会停在旧位置或隐藏。
    const mutations = new MutationObserver(() => {
      const pill = pillRef.current
      if (pill) {
        placePill(container, pill, selector, false)
      }
    })
    mutations.observe(container, { childList: true, subtree: true })

    return () => {
      observer.disconnect()
      mutations.disconnect()
    }
  }, [containerRef, selector, revision])

  return <span ref={pillRef} aria-hidden="true" className={cn('sliding-pill', className)} hidden />
}

function placePill(container: HTMLElement, pill: HTMLElement, selector: string, animate: boolean): void {
  const active = container.querySelector<HTMLElement>(selector)
  pill.hidden = !active
  if (!active) {
    pill.getAnimations?.().forEach((animation) => animation.cancel())
    delete container.dataset.pill
    return
  }

  const target: PillPosition = {
    left: active.offsetLeft,
    top: active.offsetTop,
    width: active.offsetWidth,
    height: active.offsetHeight,
  }
  if (!target.width || !target.height) {
    return
  }
  const properties = ['left', 'top', 'width', 'height'] as const
  if (container.dataset.pill === 'ready' && properties.every((key) => pill.style[key] === target[key] + 'px')) {
    return
  }

  const shouldAnimate =
    animate &&
    container.dataset.pill === 'ready' &&
    typeof pill.animate === 'function' &&
    !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  const previous = shouldAnimate ? pill.getBoundingClientRect() : null
  const origin = previous ? container.getBoundingClientRect() : null
  pill.getAnimations?.().forEach((animation) => animation.cancel())
  for (const key of properties) {
    pill.style[key] = target[key] + 'px'
  }
  container.dataset.pill = 'ready'

  if (!previous || !origin) {
    return
  }
  const deltaX = previous.left + previous.width / 2 - origin.left - target.left - target.width / 2
  const deltaY = previous.top + previous.height / 2 - origin.top - target.top - target.height / 2
  const stretch = 1 + Math.min(Math.abs(deltaX) * 0.15, 28) / target.width
  const overshoot = Math.sign(deltaX) * Math.min(Math.abs(deltaX) * 0.06, 6)

  pill.animate(
    [
      {
        transform: 'translate(' + deltaX + 'px, ' + deltaY + 'px) scale(' + previous.width / target.width + ', ' + previous.height / target.height + ')',
        offset: 0,
        easing: 'cubic-bezier(.35,.05,.25,1)',
      },
      { transform: 'translateX(' + deltaX * 0.42 + 'px) scale(' + stretch + ', .93)', offset: 0.45, easing: 'ease-out' },
      { transform: 'translateX(' + -overshoot + 'px) scale(.98, 1.02)', offset: 0.81, easing: 'ease-out' },
      { transform: 'none', offset: 1 },
    ],
    { duration: 420 },
  )
}
