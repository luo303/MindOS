import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { authUserFixture, envelope, jsonResponse } from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

const fetchMock = vi.fn()

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  fetchMock.mockReset()
})

function jsonOk(body: unknown, status = 200) {
  return Promise.resolve(jsonResponse(body, status))
}

describe('登录、注册与路由守卫', () => {
  it('未登录访问受保护页面时跳到登录页', async () => {
    renderApp('/memory', { user: null })

    expect(await screen.findByRole('heading', { name: '登录' })).toBeInTheDocument()
    expect(screen.getByLabelText('邮箱')).toBeInTheDocument()
    expect(screen.getByLabelText('密码')).toBeInTheDocument()
  })

  it('登录成功后回到原来要去的页面', async () => {
    const user = userEvent.setup()
    fetchMock.mockImplementation((input: unknown, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/auth/login' && init?.method === 'POST') {
        return jsonOk(envelope({ user: authUserFixture, expires_at: '2026-12-17T00:00:00Z' }))
      }
      return Promise.reject(new Error('unexpected request: ' + url))
    })

    renderApp('/memory', { user: null })
    await user.type(screen.getByLabelText('邮箱'), 'alice@example.com')
    await user.type(screen.getByLabelText('密码'), 'password-123')
    await user.click(screen.getByRole('button', { name: '登录' }))

    // next=/memory 生效：不是回工作台，而是回到用户原本想看的页面。
    expect(await screen.findByRole('heading', { name: '记忆' })).toBeInTheDocument()
  })

  it('登录失败显示后端给的文案', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValue(
      jsonResponse({ code: 'AUTH_INVALID_CREDENTIALS', message: '邮箱或密码不正确', request_id: 'r1' }, 401),
    )

    renderApp('/login', { user: null })
    await user.type(screen.getByLabelText('邮箱'), 'alice@example.com')
    await user.type(screen.getByLabelText('密码'), 'wrong-password')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('邮箱或密码不正确')
  })

  it('已登录时访问登录页直接进入工作台', async () => {
    renderApp('/login')

    expect(await screen.findByRole('heading', { name: '工作台' })).toBeInTheDocument()
  })

  it('注册成功即登录并进入应用', async () => {
    const user = userEvent.setup()
    fetchMock.mockImplementation((input: unknown, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/auth/register' && init?.method === 'POST') {
        return jsonOk(envelope({ user: authUserFixture, expires_at: '2026-12-17T00:00:00Z' }), 201)
      }
      return Promise.reject(new Error('unexpected request: ' + url))
    })

    renderApp('/register', { user: null })
    await user.type(screen.getByLabelText('邮箱'), 'alice@example.com')
    await user.type(screen.getByLabelText('密码'), 'password-123')
    await user.type(screen.getByLabelText('确认密码'), 'password-123')
    await user.click(screen.getByRole('button', { name: '创建账号' }))

    expect(await screen.findByRole('heading', { name: '工作台' })).toBeInTheDocument()
  })

  it('两次密码不一致时不发请求', async () => {
    const user = userEvent.setup()
    renderApp('/register', { user: null })

    await user.type(screen.getByLabelText('邮箱'), 'alice@example.com')
    await user.type(screen.getByLabelText('密码'), 'password-123')
    await user.type(screen.getByLabelText('确认密码'), 'password-456')
    await user.click(screen.getByRole('button', { name: '创建账号' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('两次输入的密码不一致')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('退出登录后回到登录页', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValue(jsonResponse(envelope({ logged_out: true })))

    renderApp('/')
    await user.click(screen.getByLabelText('账号'))
    await user.click(screen.getByRole('button', { name: '退出登录' }))

    expect(await screen.findByRole('heading', { name: '登录' })).toBeInTheDocument()
  })

  it('账号可展开查看邮箱并用 Escape 关闭', async () => {
    const user = userEvent.setup()
    renderApp('/')

    const trigger = screen.getByLabelText('账号')
    const details = trigger.closest('details')!
    expect(details).not.toHaveAttribute('open')
    await user.click(trigger)
    expect(details).toHaveAttribute('open')
    expect(within(details).getByText('alice@example.com')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(details).not.toHaveAttribute('open')
    expect(trigger).toHaveFocus()
  })
})
