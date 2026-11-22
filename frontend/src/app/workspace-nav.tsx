import { useRef } from 'react'
import { NavLink, useLocation } from 'react-router'

import { modules } from '@/app/modules'
import { SlidingPill } from '@/components/sliding-pill'

export function WorkspaceNav() {
  const { pathname } = useLocation()
  const navRef = useRef<HTMLElement>(null)

  return (
    <nav ref={navRef} aria-label="主导航" className="workspace-nav">
      <SlidingPill containerRef={navRef} active={pathname} className="workspace-nav-pill" />
      {modules.map(({ key, path, label, icon: Icon }) => (
        <NavLink key={key} to={path} end={path === '/'} className="workspace-nav-link">
          <Icon size={16} strokeWidth={1.7} aria-hidden="true" />
          <span>{label}</span>
        </NavLink>
      ))}
    </nav>
  )
}
