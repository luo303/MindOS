import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { renderApp } from '@/test/render-app'

describe('应用布局与路由', () => {
  it('工作台只提供已接入的常用入口', () => {
    renderApp('/')

    expect(screen.getByRole('heading', { name: '工作台' })).toBeInTheDocument()
    const shortcuts = screen.getByRole('navigation', { name: '快捷入口' })
    expect(within(shortcuts).getByRole('link', { name: '对话' })).toHaveAttribute('href', '/chat')
    expect(within(shortcuts).getByRole('link', { name: '知识' })).toHaveAttribute('href', '/knowledge')
  })

  it('主导航包含六个模块且顺序稳定', () => {
    renderApp('/')

    const nav = screen.getByRole('navigation', { name: '主导航' })
    const labels = within(nav)
      .getAllByRole('link')
      .map((link) => link.textContent)

    expect(labels).toEqual(['工作台', '对话', '知识', '记忆', '任务', '设置'])
  })

  it('点击导航切换页面并标记当前项', async () => {
    const user = userEvent.setup()
    renderApp('/')

    // 用「记忆」而不是「知识」：知识页会真的发请求，这个用例只关心路由与高亮。
    await user.click(screen.getByRole('link', { name: '记忆' }))

    expect(screen.getByRole('heading', { name: '记忆' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '记忆' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByText('尚未接入')).toBeInTheDocument()
  })

  it('未知路径渲染 404 且保留导航外壳', async () => {
    const user = userEvent.setup()
    renderApp('/no-such-page')

    expect(screen.getByRole('heading', { name: '页面不存在' })).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: '主导航' })).toBeInTheDocument()

    await user.click(screen.getByRole('link', { name: '返回工作台' }))
    expect(screen.getByRole('heading', { name: '工作台' })).toBeInTheDocument()
  })

  it('能直接进入任务页', async () => {
    renderApp('/tasks')

    expect(await screen.findByRole('heading', { name: '任务' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '新建待办' })).toBeInTheDocument()
  })
})
