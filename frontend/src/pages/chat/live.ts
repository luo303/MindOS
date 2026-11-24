import type { Turn } from './format'

type LiveEntry = {
  turn: Turn
  controller: AbortController
}

const entries = new Map<number, LiveEntry>()
const listeners = new Set<() => void>()

function emit() {
  listeners.forEach((listener) => listener())
}

/** 订阅当前正在生成的问答；切换路由不销毁，回来后仍能拿到。 */
export function subscribeLive(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function liveTurnOf(conversationId: number | null): Turn | null {
  if (conversationId === null) {
    return null
  }
  return entries.get(conversationId)?.turn ?? null
}

export function liveControllerOf(conversationId: number | null): AbortController | null {
  if (conversationId === null) {
    return null
  }
  return entries.get(conversationId)?.controller ?? null
}

export function publishLive(conversationId: number, turn: Turn, controller: AbortController): void {
  entries.set(conversationId, { turn, controller })
  emit()
}

export function clearLive(conversationId: number, controller: AbortController): void {
  const current = entries.get(conversationId)
  if (current?.controller !== controller) {
    return
  }
  entries.delete(conversationId)
  emit()
}
