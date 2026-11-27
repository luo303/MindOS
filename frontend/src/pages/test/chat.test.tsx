import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { conversationKeys, type ChatMessage } from '@/api/conversations'
import type { AskEvent } from '@/api/types'
import { REFUSAL_TEXT, WITHDRAWN_TEXT } from '@/pages/chat/format'
import { MarkdownAnswer } from '@/pages/chat/markdown-answer'
import { chatModelFixture, envelope, jsonResponse } from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

afterEach(() => {
  vi.unstubAllGlobals()
  // 选中的模型存在 localStorage 里，用例之间要清掉，否则会互相影响。
  localStorage.clear()
})

function sseLine(event: AskEvent): string {
  return `data: ${JSON.stringify(event)}\n\n`
}

function sseResponse(events: AskEvent[]) {
  const encoder = new TextEncoder()
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const event of events) {
        controller.enqueue(encoder.encode(sseLine(event)))
      }
      controller.close()
    },
  })
  return new Response(stream, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

function openSseResponse() {
  let streamController: ReadableStreamDefaultController<Uint8Array>
  const encoder = new TextEncoder()
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      streamController = controller
    },
  })
  return {
    response: new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } }),
    push: (event: AskEvent) => streamController.enqueue(encoder.encode(sseLine(event))),
    close: () => streamController.close(),
  }
}

function controlAnimationFrames() {
  const callbacks = new Map<number, FrameRequestCallback>()
  let frameId = 0
  let time = 0
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    frameId += 1
    callbacks.set(frameId, callback)
    return frameId
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => callbacks.delete(id))
  return () => {
    act(() => {
      time += 17
      const frame = Array.from(callbacks.values())
      callbacks.clear()
      frame.forEach((callback) => callback(time))
    })
  }
}

const hit = {
  n: 1,
  document_id: 1,
  chunk_id: 11,
  title: '集成测试笔记',
  seq: 0,
  similarity: 0.82,
  rrf_score: 0.03,
  snippet: '第一段正文。',
}

const citation = { ...hit, start_offset: 0, end_offset: 12 }

function doneEvent(runId: number): Extract<AskEvent, { type: 'done' }> {
  return {
    type: 'done',
    seq: 99,
    payload: { run_id: runId, chat_model_id: 1, latency_ms: 1200, prompt_tokens: 10, completion_tokens: 5 },
  }
}

const conversationFixture = {
  id: 1,
  title: '测试会话',
  message_count: 0,
  created_at: '2026-11-18T00:00:00Z',
  updated_at: '2026-11-18T00:00:00Z',
}

/** /chat-models、/conversations 固定返回；会话消息的 POST 响应由调用方按次排队给出。 */
function stubChatFetch(...messageResponses: Response[]) {
  stubChatFetchWithConversations([conversationFixture], ...messageResponses)
}

function stubChatFetchWithConversations(
  conversations: (typeof conversationFixture)[],
  ...messageResponses: Response[]
) {
  let messageCalls = 0
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input)
      const method = init?.method ?? 'GET'
      if (url.endsWith('/chat-models')) {
        return jsonResponse(envelope({ items: [chatModelFixture()] }))
      }
      if (url.endsWith('/conversations') && method === 'GET') {
        return jsonResponse(envelope({ items: conversations }))
      }
      if (url.endsWith('/conversations') && method === 'POST') {
        return jsonResponse(envelope(conversationFixture))
      }
      if (/\/conversations\/\d+\/messages\?/.test(url) && method === 'GET') {
        return jsonResponse(envelope({ items: [] }))
      }
      if (url.includes('/conversations/1/messages') && method === 'POST') {
        const response = messageResponses[messageCalls]
        messageCalls += 1
        if (!response) {
          throw new Error(`unexpected message call #${messageCalls}`)
        }
        return response
      }
      throw new Error(`unexpected url: ${url}`)
    }),
  )
}

/** 输入问题并点击发送。先等模型列表加载完——加载期间输入框是禁用的，直接 type 会被吞掉。 */
async function ask(user: ReturnType<typeof userEvent.setup>, question: string) {
  const textarea = await screen.findByLabelText('输入问题')
  await waitFor(() => expect(textarea).toBeEnabled())
  await user.type(textarea, question)
  await user.click(screen.getByRole('button', { name: '发送' }))
}

describe('Markdown 回答', () => {
  it('渲染 GFM，并把引用编号变成知识页链接', () => {
    render(
      <MemoryRouter>
        <MarkdownAnswer text={'**加粗**\n\n- 列表项 [1]'} citations={[citation]} />
      </MemoryRouter>,
    )
    expect(screen.getByText('加粗')).toBeInTheDocument()
    expect(screen.getByText('列表项')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '[1]' })).toHaveAttribute(
      'href',
      '/knowledge?doc=1&chunk=0',
    )
  })
})

describe('问答页', () => {
  it('右键删除时只删除弹框中确认的会话', async () => {
    const user = userEvent.setup()
    const target = { ...conversationFixture, id: 2, title: '待删除会话' }
    let conversations = [conversationFixture, target]
    const deleteRequest = vi.fn((url: string) => {
      conversations = conversations.filter((conversation) => !url.endsWith(`/conversations/${conversation.id}`))
      return jsonResponse(envelope({ deleted: true }))
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/chat-models')) return jsonResponse(envelope({ items: [chatModelFixture()] }))
      if (url.endsWith('/conversations')) return jsonResponse(envelope({ items: conversations }))
      if (url.includes('/messages?')) return jsonResponse(envelope({ items: [] }))
      if (init?.method === 'DELETE') return deleteRequest(url)
      throw new Error(`unexpected url: ${url}`)
    }))
    renderApp('/chat')
    const history = within(screen.getByRole('complementary', { name: '历史会话' }))
    const row = await history.findByRole('button', { name: target.title })
    fireEvent.contextMenu(row)
    await user.click(screen.getByRole('button', { name: '删除会话' }))
    const dialog = screen.getByRole('dialog', { name: '删除会话' })
    expect(dialog).toHaveTextContent(target.title)
    expect(deleteRequest).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: '确认删除' }))

    await waitFor(() => expect(row).not.toBeInTheDocument())
    expect(deleteRequest).toHaveBeenCalledExactlyOnceWith(`/api/v1/conversations/${target.id}`)
    expect(history.getByRole('button', { name: conversationFixture.title })).toBeVisible()
  })

  it.each(['会话', '页面'])('切换%s后不重播已有正文，只逐字追加新内容', async (destination) => {
    const sse = openSseResponse()
    const advanceFrame = controlAnimationFrames()
    const otherConversation = { ...conversationFixture, id: 2, title: '另一个会话' }
    stubChatFetchWithConversations([conversationFixture, otherConversation], sse.response)
    const user = userEvent.setup()
    const { router } = renderApp('/chat')

    try {
      await ask(user, '继续回答')
      await act(async () => sse.push({ type: 'delta', seq: 1, payload: { text: '已读' } }))
      advanceFrame()
      advanceFrame()
      expect(screen.getByText('已读')).toBeVisible()

      if (destination === '会话') {
        await user.click(screen.getByRole('button', { name: otherConversation.title }))
      } else {
        await act(() => router.navigate('/memory'))
      }
      expect(screen.queryByText('已读')).not.toBeInTheDocument()
      await act(async () => sse.push({ type: 'delta', seq: 2, payload: { text: '后台' } }))

      if (destination === '会话') {
        await user.click(screen.getByRole('button', { name: conversationFixture.title }))
      } else {
        await act(() => router.navigate('/chat'))
      }
      await screen.findByText('继续回答')
      expect(screen.getByText('已读后台')).toBeVisible()
      expect(screen.getByRole('button', { name: '停止' })).toBeVisible()

      await act(async () => sse.push({ type: 'delta', seq: 3, payload: { text: '更新' } }))
      expect(screen.getByText('已读后台')).toBeVisible()
      advanceFrame()
      expect(screen.getByText('已读后台更')).toBeVisible()
      advanceFrame()
      expect(screen.getByText('已读后台更新')).toBeVisible()
    } finally {
      await act(async () => {
        sse.push(doneEvent(7))
        sse.close()
      })
    }
  })

  it.each([
    ['会话', false],
    ['页面', false],
    ['页面', true],
  ] as const)('切换%s后，结束时已返回=%s，旧历史不能吞掉最新一轮', async (destination, returnBeforeDone) => {
    const sse = openSseResponse()
    const otherConversation = { ...conversationFixture, id: 2, title: '另一个会话' }
    stubChatFetchWithConversations([conversationFixture, otherConversation], sse.response)
    const user = userEvent.setup()
    const { router } = renderApp('/chat')
    const goBack = async () => {
      if (destination === '会话') {
        await user.click(screen.getByRole('button', { name: conversationFixture.title }))
      } else {
        await act(() => router.navigate('/chat'))
      }
    }

    await ask(user, '后台完成的问题')
    await act(async () => sse.push({ type: 'delta', seq: 1, payload: { text: '正在生成' } }))
    expect(await screen.findByText('正在生成')).toBeVisible()

    if (destination === '会话') {
      await user.click(screen.getByRole('button', { name: otherConversation.title }))
    } else {
      await act(() => router.navigate('/memory'))
    }
    expect(screen.queryByText('后台完成的问题')).not.toBeInTheDocument()

    if (returnBeforeDone) {
      await goBack()
      expect(await screen.findByText('正在生成')).toBeVisible()
    }
    await act(async () => {
      sse.push({ type: 'delta', seq: 2, payload: { text: '，已经完成。' } })
      sse.push(doneEvent(7))
      sse.close()
    })
    if (!returnBeforeDone) await goBack()

    expect(await screen.findByText('正在生成，已经完成。')).toBeVisible()
    expect(screen.getAllByText('后台完成的问题')).toHaveLength(1)
    expect(screen.getByRole('button', { name: '发送' })).toBeVisible()
  })

  it('后台完成的轮次按消息 ID 与回读历史合并，不覆盖历史里的同文问题', async () => {
    const question = '重复的问题'
    const message = (id: number, role: ChatMessage['role'], content: string): ChatMessage => ({
      id, conversation_id: 1, role, content, citations: [], seq: id, created_at: conversationFixture.created_at,
    })
    const earlier = [message(1, 'user', question), message(2, 'assistant', '以前的回答')]
    const sse = openSseResponse()
    let messages = earlier
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/chat-models')) return jsonResponse(envelope({ items: [chatModelFixture()] }))
      if (url.endsWith('/conversations')) return jsonResponse(envelope({ items: [conversationFixture] }))
      if (url.endsWith('/conversations/1/messages') && init?.method === 'POST') return sse.response
      if (url.includes('/conversations/1/messages?')) return jsonResponse(envelope({ items: messages }))
      return jsonResponse(envelope({ items: [] }))
    }))
    const user = userEvent.setup()
    const { queryClient, router } = renderApp('/chat')
    expect(await screen.findByText('以前的回答')).toBeVisible()
    await ask(user, question)
    expect(screen.getAllByText(question)).toHaveLength(2)
    await act(() => router.navigate('/memory'))
    await act(async () => {
      sse.push({ type: 'delta', seq: 1, payload: { text: '新回答（流式）' } })
      const done = doneEvent(8)
      sse.push({ ...done, payload: { ...done.payload, user_message_id: 3 } })
      sse.close()
    })
    await act(() => router.navigate('/chat'))
    expect(await screen.findByText('新回答（流式）')).toBeVisible()
    expect(screen.getAllByText(question)).toHaveLength(2)

    messages = [...earlier, message(3, 'user', question), message(4, 'assistant', '新回答（已保存）')]
    await act(() => queryClient.invalidateQueries({ queryKey: conversationKeys.messages(1) }))
    expect(await screen.findByText('新回答（已保存）')).toBeVisible()
    expect(screen.queryByText('新回答（流式）')).not.toBeInTheDocument()
    expect(screen.getAllByText(question)).toHaveLength(2)
    expect(screen.getByText('以前的回答')).toBeVisible()
  })

  it('pending 审批能恢复审批前正文，并把批准后的 delta 接在同一轮', async () => {
    const userMessage = {
      id: 1,
      conversation_id: 1,
      role: 'user' as const,
      content: '帮我建一个待办',
      citations: [],
      seq: 1,
      created_at: '2026-11-24T00:00:00Z',
    }
    const approval = {
      id: 1,
      conversation_id: 1,
      user_message_id: 1,
      chat_model_id: 1,
      tool_name: 'create_todo',
      arguments: { title: '学习计划' },
      summary: '创建待办：学习计划',
      status: 'pending' as const,
      expires_at: '2026-12-01T00:00:00Z',
      result: {},
      partial_answer: '我先查一下知识库。',
      created_at: '2026-11-24T00:00:00Z',
      updated_at: '2026-11-24T00:00:00Z',
    }
    const decision = openSseResponse()
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string, init?: RequestInit) => {
        const url = String(input)
        const method = init?.method ?? 'GET'
        if (url.endsWith('/chat-models')) {
          return jsonResponse(envelope({ items: [chatModelFixture()] }))
        }
        if (url.endsWith('/conversations') && method === 'GET') {
          return jsonResponse(envelope({ items: [conversationFixture] }))
        }
        if (url.includes('/conversations/1/messages?') && method === 'GET') {
          return jsonResponse(envelope({ items: [userMessage] }))
        }
        if (url.includes('/approvals?') && method === 'GET') {
          return jsonResponse(envelope({ items: [approval] }))
        }
        if (url.endsWith('/approvals/1/decision') && method === 'POST') {
          return decision.response
        }
        throw new Error('unexpected url: ' + url)
      }),
    )
    const user = userEvent.setup()
    const { router } = renderApp('/chat')

    expect(await screen.findByText('我先查一下知识库。')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '批准' }))
    await act(async () => {
      decision.push({ type: 'delta', seq: 1, payload: { text: '现在创建。' } })
    })
    expect(await screen.findByText('我先查一下知识库。现在创建。')).toBeVisible()

    // 审批续跑中离开页面，流继续；回来应接上同一轮，而不是重新变空。
    await act(() => router.navigate('/memory'))
    await act(async () => {
      decision.push({ type: 'delta', seq: 2, payload: { text: '后台继续。' } })
    })
    await act(() => router.navigate('/chat'))
    expect(await screen.findByText('我先查一下知识库。现在创建。后台继续。')).toBeVisible()

    await act(async () => {
      decision.push(doneEvent(12))
      decision.close()
    })
  })

  it('没有配置对话模型时给出引导，输入框不可用', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse(envelope({ items: [] }))),
    )

    renderApp('/chat')

    expect(await screen.findByText('未配置对话模型')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '去设置' })).toHaveAttribute('href', '/settings')
    expect(screen.getByLabelText('输入问题')).toBeDisabled()
  })

  it('完整流程：流式正文 → 裁决通过 → 引用可点回知识页 → 展示耗时', async () => {
    stubChatFetch(
      sseResponse([
        { type: 'retrieval', seq: 1, payload: { hits: [hit] } },
        { type: 'delta', seq: 2, payload: { text: '答案是' } },
        { type: 'delta', seq: 3, payload: { text: '这样[1]。' } },
        { type: 'verdict', seq: 4, payload: { status: 'answered', reason: '' } },
        { type: 'citation', seq: 5, payload: { items: [citation] } },
        doneEvent(7),
      ]),
    )
    const user = userEvent.setup()
    renderApp('/chat')

    await ask(user, '笔记里写了什么')

    // 正文完整拼出，且 [1] 被渲染成指向知识页深链的链接
    expect(await screen.findByText(/答案是/)).toBeInTheDocument()
    const links = await screen.findAllByRole('link', { name: '[1]' })
    expect(links.length).toBeGreaterThan(0)
    for (const link of links) {
      expect(link).toHaveAttribute('href', '/knowledge?doc=1&chunk=0')
    }
    // 依据面板与引用列表都展示文档标题
    expect(screen.getAllByText('集成测试笔记').length).toBeGreaterThan(0)
    await user.click(screen.getByText('运行信息'))
    expect(screen.getByText(/1\.2s · 输入 10 \/ 输出 5 tokens · #7/)).toBeVisible()
  })

  it('保留键盘快捷发送', async () => {
    stubChatFetch(sseResponse([
      { type: 'delta', seq: 1, payload: { text: '键盘发送成功' } },
      { type: 'verdict', seq: 2, payload: { status: 'answered', reason: '' } },
      doneEvent(8),
    ]))
    const user = userEvent.setup()
    renderApp('/chat')

    const input = screen.getByRole('textbox', { name: '输入问题' })
    await waitFor(() => expect(input).toBeEnabled())
    await user.type(input, '这条消息从键盘发送{Control>}{Enter}{/Control}')

    expect(await screen.findByText('键盘发送成功')).toBeInTheDocument()
  })

  it('模型多次检索时，依据面板合并各批次而不是覆盖', async () => {
    const second = { ...hit, n: 2, chunk_id: 12, seq: 1, title: '第二篇笔记' }
    stubChatFetch(
      sseResponse([
        { type: 'retrieval', seq: 1, payload: { hits: [hit] } },
        { type: 'retrieval', seq: 2, payload: { hits: [second] } },
        { type: 'delta', seq: 3, payload: { text: '综合两篇笔记。' } },
        { type: 'verdict', seq: 4, payload: { status: 'answered', reason: '' } },
        doneEvent(11),
      ]),
    )
    const user = userEvent.setup()
    renderApp('/chat')

    await ask(user, '两篇笔记分别说了什么')

    // 第一次检索的结果不能被第二次覆盖：依据面板要同时出现两篇。
    expect(await screen.findByText('第二篇笔记')).toBeInTheDocument()
    expect(screen.getByText('集成测试笔记')).toBeInTheDocument()
  })

  it('检索没过阈值时直接拒答，不展示任何生成内容', async () => {
    stubChatFetch(
      sseResponse([
        { type: 'retrieval', seq: 1, payload: { hits: [] } },
        { type: 'verdict', seq: 2, payload: { status: 'refused', reason: 'no_evidence' } },
        doneEvent(8),
      ]),
    )
    const user = userEvent.setup()
    renderApp('/chat')

    await ask(user, '一个库里完全没有的问题')

    expect(await screen.findByText(REFUSAL_TEXT)).toBeInTheDocument()
    expect(screen.getByText('原因：检索到的内容与问题无关')).toBeInTheDocument()
  })

  it('引用越界被判 ungrounded 时，已流出的正文被撤回文案替换', async () => {
    stubChatFetch(
      sseResponse([
        { type: 'retrieval', seq: 1, payload: { hits: [hit] } },
        { type: 'delta', seq: 2, payload: { text: '编造的内容[9]' } },
        { type: 'verdict', seq: 3, payload: { status: 'ungrounded', reason: 'unknown_citation' } },
        doneEvent(9),
      ]),
    )
    const user = userEvent.setup()
    renderApp('/chat')

    await ask(user, '会导致瞎编的问题')

    // 撤回后正文整体替换，编造的内容不能留在屏幕上
    expect(await screen.findByText(WITHDRAWN_TEXT)).toBeInTheDocument()
    expect(screen.queryByText(/编造的内容/)).not.toBeInTheDocument()
    expect(screen.getByText('原因：回答引用了不存在的来源')).toBeInTheDocument()
  })

  it('流中途出错时标记失败，不显示重试按钮', async () => {
    stubChatFetch(
      sseResponse([
        { type: 'error', seq: 1, payload: { code: 'MODEL_TIMEOUT', message: '模型响应超时', retryable: true } },
      ]),
    )
    const user = userEvent.setup()
    renderApp('/chat')

    await ask(user, '第一次会失败的问题')

    expect(await screen.findByText('没有拿到回答')).toBeInTheDocument()
    expect(screen.getByText('模型响应超时')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '重试' })).not.toBeInTheDocument()
  })
})
