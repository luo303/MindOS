import { useLayoutEffect, useRef } from 'react'
import { NavLink, useLocation } from 'react-router'

import { modules } from '@/app/modules'

type PillPosition = { left: number; top: number; width: number; height: number }

function placePill(nav: HTMLElement, animate: boolean): void {
  const active = nav.querySelector<HTMLElement>('[aria-current="page"]')
  const pill = nav.querySelector<HTMLElement>('.workspace-nav-pill')
  if (!pill) return

  pill.hidden = !active
  if (!active) {
    pill.getAnimations?.().forEach((animation) => animation.cancel())
    delete nav.dataset.pill
    return
  }

  const target: PillPosition = {
    left: active.offsetLeft,
    top: active.offsetTop,
    width: active.offsetWidth,
    height: active.offsetHeight,
  }
  if (!target.width || !target.height) return
  const properties = ['left', 'top', 'width', 'height'] as const
  if (nav.dataset.pill === 'ready' && properties.every((key) => pill.style[key] === `${target[key]}px`)) return

  const shouldAnimate = animate && nav.dataset.pill === 'ready' && typeof pill.animate === 'function'
    && !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  const previous = shouldAnimate ? pill.getBoundingClientRect() : null
  const origin = previous ? nav.getBoundingClientRect() : null
  pill.getAnimations?.().forEach((animation) => animation.cancel())
  for (const key of properties) pill.style[key] = `${target[key]}px`
  nav.dataset.pill = 'ready'

  if (!previous || !origin) return
  const deltaX = previous.left + previous.width / 2 - origin.left - target.left - target.width / 2
  const deltaY = previous.top + previous.height / 2 - origin.top - target.top - target.height / 2
  const stretch = 1 + Math.min(Math.abs(deltaX) * 0.15, 28) / target.width
  const overshoot = Math.sign(deltaX) * Math.min(Math.abs(deltaX) * 0.06, 6)

  pill.animate([
    { transform: `translate(${deltaX}px, ${deltaY}px) scale(${previous.width / target.width}, ${previous.height / target.height})`, offset: 0, easing: 'cubic-bezier(.35,.05,.25,1)' },
    { transform: `translateX(${deltaX * 0.42}px) scale(${stretch}, .93)`, offset: 0.45, easing: 'ease-out' },
    { transform: `translateX(${-overshoot}px) scale(.98, 1.02)`, offset: 0.81, easing: 'ease-out' },
    { transform: 'none', offset: 1 },
  ], { duration: 420 })
}

export function WorkspaceNav() {
  const { pathname } = useLocation()
  const navRef = useRef<HTMLElement>(null)

  useLayoutEffect(() => {
    if (navRef.current) placePill(navRef.current, true)
  }, [pathname])

  useLayoutEffect(() => {
    const nav = navRef.current
    if (!nav || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => placePill(nav, false))
    observer.observe(nav)
    nav.querySelectorAll('a').forEach((link) => observer.observe(link))
    return () => observer.disconnect()
  }, [])

  return (
    <nav ref={navRef} aria-label="主导航" className="workspace-nav">
      <span className="workspace-nav-pill" aria-hidden="true" hidden />
      {modules.map(({ key, path, label, icon: Icon }) => (
        <NavLink key={key} to={path} end={path === '/'} className="workspace-nav-link">
          <Icon size={16} strokeWidth={1.7} aria-hidden="true" />
          <span>{label}</span>
        </NavLink>
      ))}
    </nav>
  )
}
