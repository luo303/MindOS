import { API_BASE } from '@/api/client'
import { ApiError } from '@/api/errors'
import type {
  AskCitationItem,
  AskDonePayload,
  AskEvent,
  AskRetrievalItem,
  AskVerdict,
} from '@/api/types'

export type AskInput = {
  question: string
  /** 不传则由后端按"默认 → 唯一一条 → 报错"解析 */
  chatModelId?: number
  limit?: number
}

/** 事件回调。每个都是可选的：调用方只关心自己需要的几种。 */
export type AskHandlers = {
  onRetrieval?: (items: AskRetrievalItem[]) => void
  onDelta?: (text: string) => void
  onVerdict?: (status: AskVerdict, reason: string) => void
  onCitation?: (items: AskCitationItem[]) => void
  onError?: (code: string, message: string, retryable: boolean) => void
  onDone?: (payload: AskDonePayload) => void
}

/**
 * 一次流式问答。
 *
 * 【为什么不用 EventSource】
 * 它只支持 GET，无法带 JSON body，也设不了自定义头。而 /ask 的问题是 body 里的，
 * 且要 POST（有副作用、不该被缓存）。所以这里用 fetch + ReadableStream 手写
 * SSE 解析——这是与 frontend/README.md 里"用原生 EventSource"约定的
 * **一处有意偏离**，README 里已同步说明理由。
 *
 * 【为什么自己切分】
 * read() 返回的是任意大小的字节块，一个事件可能被拆成两半、也可能一次来三个。
 * 所以必须自己缓冲，按空行（\n\n）切事件。这是手写 SSE 唯一容易写错的地方。
 *
 * Promise 在流结束（收到 done 或服务端关闭）时 resolve；
 * 网络错误、HTTP 错误、用户 Abort 时 reject。
 */
export async function askStream(input: AskInput, handlers: AskHandlers, signal?: AbortSignal): Promise<void> {
  let response: Response
  try {
    response = await fetch(`${API_BASE}/ask`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify({
        question: input.question,
        ...(input.chatModelId ? { chat_model_id: input.chatModelId } : {}),
        ...(input.limit ? { limit: input.limit } : {}),
      }),
      signal,
    })
  } catch (error) {
    if (signal?.aborted) {
      throw new ApiError('请求已取消', { kind: 'aborted', cause: error })
    }
    throw new ApiError('无法连接后端服务，确认 go run ./cmd/server 已启动', {
      kind: 'network',
      cause: error,
    })
  }

  if (!response.ok) {
    throw await readEnvelopeError(response)
  }
  if (!response.body) {
    throw new ApiError('当前浏览器不支持流式响应', { kind: 'malformed' })
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) {
        break
      }
      // 去掉 \r：某些代理会把 \n\n 改写成 \r\n\r\n，不处理就永远匹配不到分隔符，
      // 表现为"请求成功但界面一直转圈"。
      buffer += decoder.decode(value, { stream: true }).replace(/\r/g, '')

      let boundary = buffer.indexOf('\n\n')
      while (boundary >= 0) {
        const chunk = buffer.slice(0, boundary)
        buffer = buffer.slice(boundary + 2)
        dispatch(chunk, handlers)
        boundary = buffer.indexOf('\n\n')
      }
    }
    // 流结束时缓冲里可能还剩最后一个事件（服务端没补空行就关闭了连接）。
    if (buffer.trim() !== '') {
      dispatch(buffer, handlers)
    }
  } finally {
    // 不 releaseLock 会让同一条连接无法被复用，也会掩盖未关闭的 reader。
    reader.releaseLock()
  }
}

function dispatch(chunk: string, handlers: AskHandlers): void {
  for (const line of chunk.split('\n')) {
    if (!line.startsWith('data: ')) {
      // 心跳与注释行（": ping"）直接跳过。
      continue
    }
    const event = parseEvent(line.slice(6))
    if (event) {
      route(event, handlers)
    }
  }
}

function parseEvent(raw: string): AskEvent | null {
  try {
    const parsed = JSON.parse(raw) as AskEvent
    if (typeof parsed?.type !== 'string') {
      return null
    }
    return parsed
  } catch {
    // 单条事件坏了不该让整个回答失败：丢掉它，后续事件照常处理。
    return null
  }
}

function route(event: AskEvent, handlers: AskHandlers): void {
  switch (event.type) {
    case 'retrieval':
      handlers.onRetrieval?.(event.payload.hits)
      break
    case 'delta':
      handlers.onDelta?.(event.payload.text)
      break
    case 'verdict':
      handlers.onVerdict?.(event.payload.status, event.payload.reason)
      break
    case 'citation':
      handlers.onCitation?.(event.payload.items)
      break
    case 'error':
      handlers.onError?.(event.payload.code, event.payload.message, event.payload.retryable)
      break
    case 'done':
      handlers.onDone?.(event.payload)
      break
    default:
      // 联合类型已穷尽：新增事件类型时这里会编译报错，提醒补分支。
      break
  }
}

async function readEnvelopeError(response: Response): Promise<ApiError> {
  const raw = await response.text()
  try {
    const payload = JSON.parse(raw) as { code?: string; message?: string; request_id?: string }
    return new ApiError(payload.message || `服务返回 HTTP ${response.status}`, {
      kind: 'http',
      status: response.status,
      code: payload.code,
      requestId: payload.request_id,
    })
  } catch {
    return new ApiError(`服务返回 HTTP ${response.status}`, { kind: 'http', status: response.status })
  }
}
