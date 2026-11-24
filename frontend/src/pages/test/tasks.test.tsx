import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Todo } from '@/api/todos'
import { envelope, jsonResponse } from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

const todo: Todo = {
  id: 17,
  title: '整理读书笔记',
  description: '汇总摘录',
  status: 'open',
  priority: 1,
  source: 'manual',
  source_ref: {},
  created_at: '2026-11-24T08:00:00Z',
  updated_at: '2026-11-24T08:00:00Z',
}

function stubTodoFetch(
  getItems: () => Todo[],
  write: (method: string, url: string, init?: RequestInit) => Response,
) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'GET' && url === '/api/v1/todos?status=open') {
      return jsonResponse(envelope({ items: getItems() }))
    }
    return write(method, url, init)
  }))
}

afterEach(() => vi.unstubAllGlobals())

describe('待办操作', () => {
  it('在右侧抽屉新建待办，取消后不保留草稿，成功后刷新列表', async () => {
    let items = [todo]
    const created = { ...todo, id: 18, title: '回顾最近的任务', description: '', priority: 2 }
    const write = vi.fn(() => {
      items = [created, todo]
      return jsonResponse(envelope(created), 201)
    })
    stubTodoFetch(() => items, write)
    const user = userEvent.setup()
    renderApp('/tasks')

    const newButton = screen.getByRole('button', { name: '新建待办' })
    await user.click(newButton)
    const initialDrawer = screen.getByRole('dialog', { name: '新建待办' })
    expect(initialDrawer).toHaveClass('drawer-panel')
    expect(within(initialDrawer).getByRole('button', { name: '创建' })).toBeDisabled()
    await user.type(within(initialDrawer).getByRole('textbox', { name: '待办标题' }), '未提交草稿')
    await user.click(within(initialDrawer).getByRole('button', { name: '取消' }))
    expect(write).not.toHaveBeenCalled()

    await user.click(newButton)
    const drawer = within(screen.getByRole('dialog', { name: '新建待办' }))
    const title = drawer.getByRole('textbox', { name: '待办标题' })
    expect(title).toHaveValue('')
    expect(screen.getByText(todo.title)).toBeVisible()
    await user.type(title, ` ${created.title} `)
    await user.click(drawer.getByRole('button', { name: '创建' }))

    expect(await screen.findByText(created.title)).toBeVisible()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(write).toHaveBeenCalledExactlyOnceWith('POST', '/api/v1/todos', expect.objectContaining({
      body: JSON.stringify({ title: created.title, description: '', priority: 2, due_at: null }),
    }))
  })

  it('编辑抽屉预填原值，保存失败保留改动并可重试', async () => {
    let current = todo
    const updated = { ...todo, title: '整理本周读书笔记' }
    const write = vi.fn()
      .mockImplementationOnce(() => jsonResponse({ code: 'INTERNAL_ERROR', message: '保存失败，请重试' }, 500))
      .mockImplementationOnce(() => {
        current = updated
        return jsonResponse(envelope(updated))
      })
    stubTodoFetch(() => [current], write)
    const user = userEvent.setup()
    renderApp('/tasks')

    await user.click(await screen.findByRole('button', { name: '编辑待办' }))
    const dialog = screen.getByRole('dialog', { name: '编辑待办' })
    expect(dialog).toHaveClass('drawer-panel')
    const editor = within(dialog)
    const title = editor.getByRole('textbox', { name: '待办标题' })
    expect(title).toHaveValue(todo.title)
    expect(editor.getByRole('textbox', { name: '待办描述' })).toHaveValue(todo.description)
    const save = editor.getByRole('button', { name: '保存' })
    await user.clear(title)
    await user.type(title, updated.title)
    await user.click(save)

    expect(await editor.findByRole('alert')).toHaveTextContent('保存失败，请重试')
    expect(title).toHaveValue(updated.title)
    await user.click(save)
    expect(await screen.findByText(updated.title)).toBeVisible()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(write).toHaveBeenCalledTimes(2)
    expect(write).toHaveBeenLastCalledWith('PATCH', `/api/v1/todos/${todo.id}`, expect.objectContaining({
      body: JSON.stringify({ title: updated.title, description: todo.description, priority: todo.priority, due_at: null }),
    }))
  })

  it('在弹框确认删除，取消不发请求，失败可在弹框重试', async () => {
    let items = [todo]
    const write = vi.fn()
      .mockImplementationOnce(() => jsonResponse({ code: 'INTERNAL_ERROR', message: '删除失败，请重试' }, 500))
      .mockImplementationOnce(() => {
        items = []
        return jsonResponse(envelope({ deleted: true }))
      })
    stubTodoFetch(() => items, write)
    const user = userEvent.setup()
    renderApp('/tasks')

    const rowDelete = await screen.findByRole('button', { name: '删除待办' })
    await user.click(rowDelete)
    const dialog = screen.getByRole('dialog', { name: '删除待办' })
    expect(dialog).toHaveTextContent(`确定删除「${todo.title}」？`)
    expect(rowDelete).toBeVisible()
    await user.click(within(dialog).getByRole('button', { name: '取消' }))
    expect(write).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    await user.click(rowDelete)
    const confirmation = within(screen.getByRole('dialog', { name: '删除待办' }))
    const confirm = confirmation.getByRole('button', { name: '确认删除' })
    await user.click(confirm)
    expect(await confirmation.findByRole('alert')).toHaveTextContent('删除失败，请重试')
    expect(screen.getByRole('button', { name: '删除待办' })).toBeVisible()
    await user.click(confirm)

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByText('没有符合条件的待办')).toBeVisible()
    expect(write).toHaveBeenCalledTimes(2)
    expect(write).toHaveBeenLastCalledWith('DELETE', `/api/v1/todos/${todo.id}`, expect.any(Object))
  })
})
