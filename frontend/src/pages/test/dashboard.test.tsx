import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { documentFixture, documentListFixture, envelope, jsonResponse } from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

afterEach(() => vi.unstubAllGlobals())

describe('工作台预览', () => {
  it('复用真实文档深链和未完成待办，不展示已完成待办', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      if (input.startsWith('/api/v1/documents?')) {
        expect(input).toBe('/api/v1/documents?limit=3&offset=0')
        return jsonResponse(envelope(documentListFixture([documentFixture({ id: 7, title: '设计笔记' })])))
      }
      if (input === '/api/v1/todos') {
        return jsonResponse(envelope({ items: [
          { id: 1, title: '待整理的笔记', status: 'in_progress' },
          { id: 2, title: '已归档的笔记', status: 'done' },
        ] }))
      }
      throw new Error(`unexpected request: ${input}`)
    }))
    renderApp('/')

    expect(await screen.findByRole('link', { name: /设计笔记/ })).toHaveAttribute('href', '/knowledge?doc=7')
    expect(await screen.findByText('待整理的笔记')).toBeInTheDocument()
    expect(screen.queryByText('已归档的笔记')).not.toBeInTheDocument()
  })

  it('文档加载失败不影响待办，重试后展示正确的空态', async () => {
    const user = userEvent.setup()
    const documents = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ code: 'INTERNAL', message: '数据库暂不可用', request_id: 'doc-preview' }, 500))
      .mockResolvedValueOnce(jsonResponse(envelope(documentListFixture([]))))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      if (input.startsWith('/api/v1/documents?')) return documents()
      if (input === '/api/v1/todos') return jsonResponse(envelope({ items: [] }))
      throw new Error(`unexpected request: ${input}`)
    }))
    renderApp('/')

    expect(await screen.findByRole('alert')).toHaveTextContent('数据库暂不可用')
    expect(await screen.findByText('没有待办')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('还没有文档')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
