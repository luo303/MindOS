import type { ChatMessage } from '@/api/conversations'
import type { AskVerdict } from '@/api/types'

import { REFUSAL_TEXT, WITHDRAWN_TEXT, type Turn } from './format'

/** 选中的模型存本地：这是"本机偏好"，不该占一个后端字段。 */
const MODEL_STORAGE_KEY = 'mindos.chat.modelId'
const CONVERSATION_STORAGE_KEY = 'mindos.chat.conversationId'

export function readStoredModelId(): number | null {
  try {
    const raw = localStorage.getItem(MODEL_STORAGE_KEY)
    if (!raw) {
      return null
    }
    const parsed = Number(raw)
    return Number.isFinite(parsed) && parsed > 0 ? parsed : null
  } catch {
    // 隐私模式下 localStorage 会抛异常，不值得为此让页面挂掉。
    return null
  }
}

export function readStoredConversationId(): number | null {
  try {
    const raw = localStorage.getItem(CONVERSATION_STORAGE_KEY)
    const parsed = Number(raw)
    return Number.isFinite(parsed) && parsed > 0 ? parsed : null
  } catch {
    return null
  }
}

export function rememberConversationId(id: number | null) {
  try {
    if (id === null) {
      localStorage.removeItem(CONVERSATION_STORAGE_KEY)
    } else {
      localStorage.setItem(CONVERSATION_STORAGE_KEY, String(id))
    }
  } catch {
    // 隐私模式存不了不影响当前会话。
  }
}

export function newId(): string {
  return Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8)
}

/**
 * 把后端的 messages 还原成界面上的"一问一答"。
 *
 * 后端一条消息一行；界面按 user 开新 turn，紧随其后的 assistant 填进 answer。
 */
export function turnsFromMessages(messages: ChatMessage[]): Turn[] {
  const turns: Turn[] = []
  let current: Turn | null = null
  for (const message of messages) {
    if (message.role === 'user') {
      current = {
        id: 'msg-' + message.id,
        messageId: message.id,
        question: message.content,
        answer: '',
        phase: 'settled',
        hits: [],
        citations: [],
      }
      turns.push(current)
      continue
    }
    if (!current) {
      continue
    }
    current.answer = message.content
    // 后端正常返回数组；这里再兜一层，避免旧缓存/旧版本把 base64 字符串
    // 塞进来时整页崩在 citations.map。
    current.citations = Array.isArray(message.citations) ? message.citations : []
    if (message.error_code) {
      current.phase = 'failed'
      current.error = {
        code: message.error_code,
        message: message.error_message ?? '模型返回失败',
      }
    } else {
      current.phase = 'settled'
    }
  }
  return turns
}

/** 把正在生成的本地轮次合并进历史，避免切页回来后看到旧的用户消息。 */
export function withLiveTurn(turns: Turn[], live: Turn): Turn[] {
  let index = turns.findIndex((turn) =>
    turn.id === live.id || (live.messageId !== undefined && turn.messageId === live.messageId),
  )
  const last = turns.at(-1)
  if (
    index < 0 && live.messageId === undefined && last?.messageId !== undefined &&
    last.phase === 'settled' && last.answer === '' && last.question === live.question
  ) {
    index = turns.length - 1
  }
  if (index < 0) {
    return [...turns, live]
  }
  const merged = {
    ...live,
    messageId: live.messageId ?? turns[index].messageId,
  }
  return turns.map((turn, i) => (i === index ? merged : turn))
}

export function withStreamResults(history: Turn[], results: Turn[]): Turn[] {
  const firstMessageId = history[0]?.messageId
  const byMessageId = new Map(history.map((turn) => [turn.messageId, turn]))
  return results.reduce((turns, result) => {
    if (firstMessageId !== undefined && result.messageId !== undefined && result.messageId < firstMessageId) {
      return turns
    }
    const stored = byMessageId.get(result.messageId)
    const view = result.phase === 'settled' && stored?.phase === 'settled' && stored.answer
      ? { ...result, answer: stored.answer, citations: stored.citations }
      : result
    return withLiveTurn(turns, view)
  }, history)
}

export function rememberModelId(id: number) {
  localStorage.setItem(MODEL_STORAGE_KEY, String(id))
}

export function applyVerdict(turn: Turn, status: AskVerdict, reason: string): Turn {
  switch (status) {
    case 'answered':
      return { ...turn, verdict: status, reason }
    case 'refused':
      return { ...turn, verdict: status, reason, phase: 'refused', answer: turn.answer || REFUSAL_TEXT }
    case 'ungrounded':
      return { ...turn, verdict: status, reason, phase: 'withdrawn', answer: WITHDRAWN_TEXT }
    default:
      return { ...turn, verdict: status, reason }
  }
}
