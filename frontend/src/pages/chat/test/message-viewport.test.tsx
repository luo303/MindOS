import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { MessageViewport } from '../message-viewport'

afterEach(() => vi.unstubAllGlobals())

function renderViewport(streaming: boolean, reducedMotion = false) {
  let onResize: () => void = () => undefined
  let scrollHeight = 1000
  const disconnect = vi.fn()
  const scrollTo = vi.fn()

  vi.stubGlobal('ResizeObserver', class {
    constructor(notify: () => void) { onResize = notify }
    observe() { return undefined }
    disconnect = disconnect
  })
  vi.stubGlobal('matchMedia', () => ({ matches: reducedMotion }))

  const view = render(<MessageViewport streaming={streaming}>回答内容</MessageViewport>)
  const viewport = screen.getByRole('region', { name: '会话消息' })
  Object.defineProperties(viewport, {
    clientHeight: { value: 400 },
    scrollHeight: { get: () => scrollHeight },
    scrollTo: { value: scrollTo },
  })

  function resize(height: number) {
    scrollHeight = height
    act(onResize)
  }

  function scroll(top: number) {
    viewport.scrollTop = top
    fireEvent.scroll(viewport)
  }

  resize(scrollHeight)
  return { ...view, viewport, resize, scroll, scrollTo, disconnect }
}

describe('对话消息滚动', () => {
  it('离底部超过 160px 才显示按钮，上翻时不跟随，返回后继续跟随', async () => {
    const { viewport, resize, scroll, unmount, disconnect } = renderViewport(true)
    const user = userEvent.setup()
    expect(viewport.scrollTop).toBe(600)
    expect(screen.queryByRole('button', { name: '回到底部' })).not.toBeInTheDocument()

    scroll(440)
    expect(screen.queryByRole('button', { name: '回到底部' })).not.toBeInTheDocument()
    scroll(439)
    const jump = screen.getByRole('button', { name: '回到底部' })

    resize(1100)
    expect(viewport.scrollTop).toBe(439)
    await user.click(jump)
    expect(viewport.scrollTop).toBe(700)
    expect(viewport).toHaveFocus()
    expect(screen.queryByRole('button', { name: '回到底部' })).not.toBeInTheDocument()

    resize(1200)
    expect(viewport.scrollTop).toBe(800)
    unmount()
    expect(disconnect).toHaveBeenCalledOnce()
  })

  it('非生成状态平滑滚动，实际到达底部后隐藏按钮', async () => {
    const { viewport, scroll, scrollTo } = renderViewport(false)
    scroll(200)
    await userEvent.click(screen.getByRole('button', { name: '回到底部' }))
    expect(scrollTo).toHaveBeenCalledWith({ top: 600, behavior: 'smooth' })
    expect(viewport.scrollTop).toBe(200)
    scroll(600)
    expect(screen.queryByRole('button', { name: '回到底部' })).not.toBeInTheDocument()
  })

  it('减少动态效果时直接回底', async () => {
    const { viewport, scroll, scrollTo } = renderViewport(false, true)
    scroll(200)
    await userEvent.click(screen.getByRole('button', { name: '回到底部' }))
    expect(viewport.scrollTop).toBe(600)
    expect(scrollTo).not.toHaveBeenCalled()
  })
})
