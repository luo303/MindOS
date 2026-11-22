import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { Card } from '@/components/ui/card'
import { renderApp } from '@/test/render-app'

afterEach(() => vi.unstubAllGlobals())

function mockPillGeometry(reduce: boolean) {
  const animate = vi.fn<(frames: Keyframe[], options: KeyframeAnimationOptions) => Animation>()
  vi.stubGlobal('matchMedia', () => ({ matches: reduce }))
  Object.defineProperty(HTMLElement.prototype, 'animate', { configurable: true, value: animate })
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(68)
  vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(40)
  vi.spyOn(HTMLElement.prototype, 'offsetTop', 'get').mockReturnValue(4)
  vi.spyOn(HTMLElement.prototype, 'offsetLeft', 'get').mockImplementation(function (this: HTMLElement) {
    return [...(this.parentElement?.querySelectorAll('a') ?? [])].findIndex((link) => link === this) * 72 + 4
  })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    return new DOMRect(parseFloat(this.style.left) || 0, parseFloat(this.style.top) || 0, 68, 40)
  })
  return animate
}

describe('工作台玻璃动效', () => {
  afterEach(() => Reflect.deleteProperty(HTMLElement.prototype, 'animate'))

  it('用同一个胶囊跟随路由，移动时拉伸，浏览器后退也能归位', async () => {
    const animate = mockPillGeometry(false)
    const user = userEvent.setup()
    const { router } = renderApp('/memory')
    const nav = screen.getByRole('navigation', { name: '主导航' })
    const pill = nav.querySelector('.workspace-nav-pill')
    expect(pill).toHaveStyle({ left: '220px' })

    await user.click(within(nav).getByRole('link', { name: '工作台' }))
    expect(pill).toHaveStyle({ left: '4px' })
    const [frames] = animate.mock.calls[0]
    expect(frames.map(({ offset }) => offset)).toEqual([0, 0.45, 0.81, 1])
    expect(frames[0].transform).toContain('translate(216px')
    expect(frames[1].transform).toContain('scale(1.41')
    expect(frames[2].transform).toContain('translateX(-6px)')

    await act(() => router.navigate(-1))
    expect(pill).toHaveStyle({ left: '220px' })
    expect(animate).toHaveBeenCalledTimes(2)
  })

  it('减少动态效果时当前项直接定位，不播放动画', async () => {
    const animate = mockPillGeometry(true)
    const user = userEvent.setup()
    renderApp('/memory')

    const nav = screen.getByRole('navigation', { name: '主导航' })
    await user.click(within(nav).getByRole('link', { name: '工作台' }))
    expect(nav.querySelector('.workspace-nav-pill')).toHaveStyle({ left: '4px' })
    expect(animate).not.toHaveBeenCalled()
  })

  it('鼠标位于边缘时限制倾斜角，离开清除状态，触屏不触发动效', () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue(new DOMRect(50, 60, 200, 100))
    const { container } = render(<Card interactive>文档</Card>)
    const card = container.querySelector<HTMLElement>('.glass-card')!

    fireEvent.pointerMove(card, { pointerType: 'mouse', clientX: 250, clientY: 60 })
    expect(card).toHaveAttribute('data-lit')
    expect(card.style.getPropertyValue('--glass-x')).toBe('100%')
    expect(card.style.getPropertyValue('--glass-y')).toBe('0%')
    expect(Math.hypot(...['--glass-rx', '--glass-ry'].map((key) => parseFloat(card.style.getPropertyValue(key))))).toBeLessThan(4)

    fireEvent.pointerLeave(card)
    expect(card).not.toHaveAttribute('data-lit')
    fireEvent.pointerMove(card, { pointerType: 'touch', clientX: 100, clientY: 100 })
    expect(card).not.toHaveAttribute('data-lit')
  })
})
