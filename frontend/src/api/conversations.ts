import { streamSSE, type AskHandlers } from '@/api/ask'
import { apiRequest } from '@/api/client'
import type { AskCitationItem } from '@/api/types'

/** Conversation 是后端 conversations 表的一行（列表/创建接口的返回）。 */
export type Conversation = {
  id: number
  title: string
  message_count: number
  created_at: string
  updated_at: string
}

/** ChatMessage 是后端 messages 表的一行。 */
export type ChatMessage = {
  id: number
  conversation_id: number
  role: 'user' | 'assistant'
  content: string
  citations: AskCitationItem[]
  // 失败时后端会持久化一条 assistant 消息，刷新后靠这两个字段还原失败卡片。
  error_code?: string | null
  error_message?: string | null
  seq: number
  created_at: string
}

export const conversationKeys = {
  all: ['conversations'] as const,
  messages: (id: number) => ['conversations', id, 'messages'] as const,
  streamResults: (id: number) => ['conversations', id, 'stream-results'] as const,
}

export async function fetchConversations(signal?: AbortSignal): Promise<Conversation[]> {
  const data = await apiRequest<{ items: Conversation[] | null }>('/conversations', { signal })
  return data.items ?? []
}

export function createConversation(title?: string): Promise<Conversation> {
  return apiRequest<Conversation>('/conversations', {
    method: 'POST',
    body: title ? { title } : {},
  })
}

/** 删除会话；PG 级联消息，ES 副本由后端 outbox 清理。 */
export function deleteConversation(conversationId: number): Promise<{ deleted: boolean }> {
  return apiRequest<{ deleted: boolean }>(`/conversations/${conversationId}`, {
    method: 'DELETE',
  })
}

export async function fetchMessages(conversationId: number, signal?: AbortSignal): Promise<ChatMessage[]> {
  const data = await apiRequest<{ items: ChatMessage[] | null }>(
    `/conversations/${conversationId}/messages?limit=50`,
    { signal },
  )
  return data.items ?? []
}

/** 在会话里发一条消息；事件形状与 /ask 完全一致。 */
export function sendConversationMessage(
  conversationId: number,
  input: { question: string; chatModelId?: number },
  handlers: AskHandlers,
  signal?: AbortSignal,
): Promise<void> {
  return streamSSE(
    `/conversations/${conversationId}/messages`,
    {
      question: input.question,
      ...(input.chatModelId ? { chat_model_id: input.chatModelId } : {}),
    },
    handlers,
    signal,
  )
}
