import type { PointerEvent } from 'react'

const GLASS_POINTER_QUERY = '(hover: hover) and (pointer: fine) and (prefers-reduced-motion: no-preference)'

export function followGlassPointer(event: PointerEvent<HTMLElement>, tilt = false): void {
  if (event.pointerType !== 'mouse' || !window.matchMedia?.(GLASS_POINTER_QUERY).matches) return
  const surface = event.currentTarget
  const rect = surface.getBoundingClientRect()
  if (!rect.width || !rect.height) return
  const relativeX = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width))
  const relativeY = Math.min(1, Math.max(0, (event.clientY - rect.top) / rect.height))
  surface.style.setProperty('--glass-x', `${relativeX * 100}%`)
  surface.style.setProperty('--glass-y', `${relativeY * 100}%`)
  if (tilt) {
    surface.style.setProperty('--glass-rx', `${(0.5 - relativeY) * 5}deg`)
    surface.style.setProperty('--glass-ry', `${(relativeX - 0.5) * 5}deg`)
  }
  surface.dataset.lit = ''
}

export function clearGlassPointer(event: PointerEvent<HTMLElement>): void {
  delete event.currentTarget.dataset.lit
}
