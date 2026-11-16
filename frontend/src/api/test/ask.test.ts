import { afterEach, describe, expect, it, vi } from 'vitest'

import { askStream } from '@/api/ask'
import { ApiError } from '@/api/errors'
import type { AskEvent } from '@/api/types'
import { jsonResponse } from '@/test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

/** 把若干字符串块包成一个 SSE 响应体。分块方式完全由用例控制。 */
function sseResponse(chunks: string[]) {
  const encoder = new TextEncoder()
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk))
      }
      controller.close()
    },
  })
  return new Response(stream, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

function sseLine(event: AskEvent): string {
  return `data: ${JSON.stringify(event)}\n\n`
}

describe('askStream 的 SSE 解析', () => {
  it('完整事件流按序分发到各个回调', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        sseResponse([
          sseLine({ type: 'retrieval', seq: 1, payload: { hits: [] } }),
          sseLine({ type: 'delta', seq: 2, payload: { text: '你好' } }),
          sseLine({ type: 'delta', seq: 3, payload: { text: '世界' } }),
          sseLine({ type: 'verdict', seq: 4, payload: { status: 'answered', reason: '' } }),
          sseLine({ type: 'citation', seq: 5, payload: { items: [] } }),
          sseLine({
            type: 'done',
            seq: 6,
            payload: { run_id: 7, chat_model_id: 1, latency_ms: 1200, prompt_tokens: 10, completion_tokens: 5 },
          }),
        ]),
      ),
    )

    const events: string[] = []
    await askStream(
      { question: '测试' },
      {
        onRetrieval: () => events.push('retrieval'),
        onDelta: (text) => events.push(`delta:${text}`),
        onVerdict: (status) => events.push(`verdict:${status}`),
        onCitation: () => events.push('citation'),
        onDone: (payload) => events.push(`done:${payload.run_id}`),
      },
    )

    expect(events).toEqual(['retrieval', 'delta:你好', 'delta:世界', 'verdict:answered', 'citation', 'done:7'])
  })

  it('一个事件被拆到两个 read 块里也能正确拼回', async () => {
    const full = sseLine({ type: 'delta', seq: 1, payload: { text: '完整的一句话' } })
    const half = Math.floor(full.length / 2)
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => sseResponse([full.slice(0, half), full.slice(half)])),
    )

    const deltas: string[] = []
    await askStream({ question: 'q' }, { onDelta: (text) => deltas.push(text) })

    expect(deltas).toEqual(['完整的一句话'])
  })

  it('代理把分隔符改写成 \\r\\n\\r\\n 也能切出事件', async () => {
    const line = `data: ${JSON.stringify({ type: 'delta', seq: 1, payload: { text: 'ab' } })}\r\n\r\n`
    vi.stubGlobal('fetch', vi.fn(async () => sseResponse([line])))

    const deltas: string[] = []
    await askStream({ question: 'q' }, { onDelta: (text) => deltas.push(text) })

    expect(deltas).toEqual(['ab'])
  })

  it('坏掉的事件行被跳过，后续事件照常处理', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        sseResponse([
          'data: {不是合法JSON\n\n',
          ': ping\n\n', // 心跳注释行
          sseLine({ type: 'delta', seq: 2, payload: { text: '幸存' } }),
        ]),
      ),
    )

    const deltas: string[] = []
    await askStream({ question: 'q' }, { onDelta: (text) => deltas.push(text) })

    expect(deltas).toEqual(['幸存'])
  })

  it('服务端没补最后一个空行就关闭连接，缓冲里的事件仍要分发', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        sseResponse([`data: ${JSON.stringify({ type: 'delta', seq: 1, payload: { text: '尾巴' } })}`]),
      ),
    )

    const deltas: string[] = []
    await askStream({ question: 'q' }, { onDelta: (text) => deltas.push(text) })

    expect(deltas).toEqual(['尾巴'])
  })

  it('非 2xx 时把后端信封翻成带 request_id 的 ApiError', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse({ code: 'NO_DEFAULT_MODEL', message: '没有配置默认对话模型', request_id: 'req-9' }, 409),
      ),
    )

    const error = await askStream({ question: 'q' }, {}).catch((e: unknown) => e)

    expect(error).toBeInstanceOf(ApiError)
    const apiError = error as ApiError
    expect(apiError.kind).toBe('http')
    expect(apiError.status).toBe(409)
    expect(apiError.code).toBe('NO_DEFAULT_MODEL')
    expect(apiError.requestId).toBe('req-9')
  })

  it('连接失败时报网络错误，用户主动停止时报 aborted', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )
    const networkError = await askStream({ question: 'q' }, {}).catch((e: unknown) => e)
    expect((networkError as ApiError).kind).toBe('network')

    const controller = new AbortController()
    controller.abort()
    const abortError = await askStream({ question: 'q' }, {}, controller.signal).catch((e: unknown) => e)
    expect((abortError as ApiError).kind).toBe('aborted')
  })
})
