import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { envelope, jsonResponse, chatModelFixture, settingsFixture, systemStatusFixture } from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

afterEach(() => {
  vi.unstubAllGlobals()
})

/** 按路径分流三个接口，返回真实的成功响应。 */
function stubSuccessFetch(getModels = () => [chatModelFixture()]) {
  const fetchMock = vi.fn(async (input: string) => {
    if (input.endsWith('/system/status')) {
      return jsonResponse(envelope(systemStatusFixture()))
    }
    if (input.endsWith('/settings')) {
      return jsonResponse(envelope(settingsFixture()))
    }
    if (input.endsWith('/chat-models')) {
      return jsonResponse(envelope({ items: getModels() }))
    }
    throw new Error(`unexpected url: ${input}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('设置页', () => {
  it('选择另一个模型时，确认弹框删除新的目标', async () => {
    const user = userEvent.setup()
    const primary = chatModelFixture()
    const target = chatModelFixture({ id: primary.id + 1, name: '试用模型', is_default: false })
    let models = [primary, target]
    const get = stubSuccessFetch(() => models).getMockImplementation()
    const remove = vi.fn((url: string) => {
      const id = Number(url.split('/').at(-1))
      models = models.filter((model) => model.id !== id)
      return jsonResponse(envelope({ id }))
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      return init?.method === 'DELETE' ? remove(url) : get?.(url)
    }))
    renderApp('/settings')

    await user.click(await screen.findByRole('button', { name: `删除 ${primary.name}` }))
    const first = screen.getByRole('dialog', { name: '删除对话模型' })
    expect(first).toHaveTextContent(primary.name)
    await user.click(within(first).getByRole('button', { name: '取消' }))
    expect(remove).not.toHaveBeenCalled()

    const row = screen.getByRole('button', { name: `删除 ${target.name}` })
    await user.click(row)
    const dialog = screen.getByRole('dialog', { name: '删除对话模型' })
    expect(dialog).toHaveTextContent(target.name)
    await user.click(within(dialog).getByRole('button', { name: '确认删除' }))

    await waitFor(() => expect(row).not.toBeInTheDocument())
    expect(remove).toHaveBeenCalledExactlyOnceWith(`/api/v1/chat-models/${target.id}`)
    expect(screen.getByRole('button', { name: `删除 ${primary.name}` })).toBeVisible()
  })

  it('加载中显示骨架而不是空白', () => {
    // 永不 resolve：停在 pending 态才能断言加载态
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))

    renderApp('/settings')

    expect(screen.getAllByText('正在加载')).toHaveLength(2)
  })

  it('成功时展示依赖、迁移与脱敏配置', async () => {
    stubSuccessFetch()

    renderApp('/settings')

    expect(await screen.findByText('PostgreSQL')).toBeInTheDocument()
    // postgres、pgvector、对象存储三项都是 up
    expect(screen.getAllByText('正常')).toHaveLength(3)
    expect(screen.getByText('对象存储 MinIO')).toBeInTheDocument()
    expect(screen.getByText('已是最新')).toBeInTheDocument()
    expect(screen.getByText(/当前版本 1 \/ 代码最新 1/)).toBeInTheDocument()
    expect(screen.getByText('未配置模型 · 未配置')).toBeInTheDocument()
    expect(screen.getByText('200,000')).toBeInTheDocument()
    // 对话模型卡片独立请求 /chat-models，也要渲染出来
    expect(await screen.findByText('讯飞 Spark')).toBeInTheDocument()
    expect(screen.getByText('Key ****wxyz')).toBeInTheDocument()
  })

  it('对象存储故障时单独报出，不牵连其他依赖', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string) => {
        if (input.endsWith('/system/status')) {
          return jsonResponse(
            envelope(
              systemStatusFixture({
                storage: { status: 'down', detail: 'endpoint unreachable or credentials rejected' },
              }),
            ),
          )
        }
        return jsonResponse(envelope(settingsFixture()))
      }),
    )

    renderApp('/settings')

    expect(await screen.findByText('对象存储 MinIO')).toBeInTheDocument()
    // 只剩 postgres 与 pgvector 是"正常"
    expect(screen.getAllByText('正常')).toHaveLength(2)
    expect(screen.getByText('不可用')).toBeInTheDocument()
    expect(screen.getByText('endpoint unreachable or credentials rejected')).toBeInTheDocument()
  })

  it('后端未启动时两块卡片各自给出失败原因和重试入口', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )

    renderApp('/settings')

    expect(await screen.findByText('读取系统状态失败')).toBeInTheDocument()
    expect(screen.getByText('读取配置失败')).toBeInTheDocument()
    // 三块卡片（系统状态 / 运行配置 / 对话模型）各自报一次
    expect(screen.getAllByText(/无法连接后端服务/)).toHaveLength(3)
    // 对话模型卡片不提供重试按钮，重试入口仍只有两个
    expect(screen.getAllByRole('button', { name: '重试' })).toHaveLength(2)
  })

  it('非 2xx 时展示后端错误码对应的文案与 request_id', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse({ code: 'INTERNAL', message: 'internal server error', request_id: 'req-500' }, 500),
      ),
    )

    renderApp('/settings')

    // 三个请求拿到同一份错误体，三块卡片都会展示（不是只报第一块）
    expect(await screen.findAllByText(/internal server error（request_id: req-500）/)).toHaveLength(3)
  })

  it('重试会重新请求并恢复正常展示', async () => {
    const user = userEvent.setup()
    let attempt = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string) => {
        attempt += 1
        // 前两次（首屏两个请求）失败，重试后的请求成功
        if (attempt <= 2) {
          throw new TypeError('Failed to fetch')
        }
        if (input.endsWith('/system/status')) {
          return jsonResponse(envelope(systemStatusFixture()))
        }
        return jsonResponse(envelope(settingsFixture()))
      }),
    )

    renderApp('/settings')

    const retryButtons = await screen.findAllByRole('button', { name: '重试' })
    await user.click(retryButtons[0] as HTMLElement)

    expect(await screen.findByText('PostgreSQL')).toBeInTheDocument()
    expect(screen.queryByText('读取系统状态失败')).not.toBeInTheDocument()
  })
})
