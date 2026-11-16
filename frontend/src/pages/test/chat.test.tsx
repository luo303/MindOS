import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { AskEvent } from '@/api/types'
import { REFUSAL_TEXT, WITHDRAWN_TEXT } from '@/pages/chat/format'
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

function doneEvent(runId: number): AskEvent {
  return {
    type: 'done',
    seq: 99,
    payload: { run_id: runId, chat_model_id: 1, latency_ms: 1200, prompt_tokens: 10, completion_tokens: 5 },
  }
}

/** /chat-models 固定返回一条配置；/ask 的响应由调用方按次排队给出。 */
function stubChatFetch(...askResponses: Response[]) {
  let askCalls = 0
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string) => {
      if (input.endsWith('/chat-models')) {
        return jsonResponse(envelope({ items: [chatModelFixture()] }))
      }
      if (input.endsWith('/ask')) {
        const response = askResponses[askCalls]
        askCalls += 1
        if (!response) {
          throw new Error(`unexpected /ask call #${askCalls}`)
        }
        return response
      }
      throw new Error(`unexpected url: ${input}`)
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

describe('问答页', () => {
  it('没有配置对话模型时给出引导，输入框不可用', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse(envelope({ items: [] }))),
    )

    renderApp('/chat')

    expect(await screen.findByText(/还没有可用的对话模型配置/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '去设置' })).toBeInTheDocument()
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
    const links = screen.getAllByRole('link', { name: '[1]' })
    expect(links.length).toBeGreaterThan(0)
    for (const link of links) {
      expect(link).toHaveAttribute('href', '/knowledge?doc=1&chunk=0')
    }
    // 依据面板与引用列表都展示文档标题
    expect(screen.getAllByText('集成测试笔记').length).toBeGreaterThan(0)
    expect(screen.getByText(/1\.2s · 输入 10 \/ 输出 5 tokens · #7/)).toBeInTheDocument()
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

  it('流中途出错时标记失败，点重试用同一问题再来一次', async () => {
    stubChatFetch(
      sseResponse([
        { type: 'error', seq: 1, payload: { code: 'MODEL_TIMEOUT', message: '模型响应超时', retryable: true } },
      ]),
      sseResponse([
        { type: 'delta', seq: 1, payload: { text: '重试成功' } },
        { type: 'verdict', seq: 2, payload: { status: 'answered', reason: '' } },
        { type: 'citation', seq: 3, payload: { items: [citation] } },
        doneEvent(10),
      ]),
    )
    const user = userEvent.setup()
    renderApp('/chat')

    await ask(user, '第一次会失败的问题')

    expect(await screen.findByText('没有拿到回答')).toBeInTheDocument()
    expect(screen.getByText('模型响应超时')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '重试' }))

    expect(await screen.findByText(/重试成功/)).toBeInTheDocument()
  })
})
