import { apiRequest } from '@/api/client'
import type { ChatModel, ChatModelInput, ChatModelProbe } from '@/api/types'

/**
 * 对话模型配置的接口封装。
 *
 * 列表与设置页共用同一个 queryKey，改完一处两处一起失效——
 * 否则会出现"设置页删了，提问页下拉里还在"这种要刷新才好的状态。
 */
export const chatModelKeys = {
  all: ['chat-models'] as const,
}

export async function fetchChatModels(signal?: AbortSignal): Promise<ChatModel[]> {
  const data = await apiRequest<{ items: ChatModel[] | null }>('/chat-models', { signal })
  return data.items ?? []
}

export function createChatModel(input: ChatModelInput): Promise<ChatModel> {
  return apiRequest<ChatModel>('/chat-models', { method: 'POST', body: input })
}

export function updateChatModel(id: number, input: ChatModelInput): Promise<ChatModel> {
  return apiRequest<ChatModel>(`/chat-models/${id}`, { method: 'PATCH', body: input })
}

export function deleteChatModel(id: number): Promise<{ id: number }> {
  return apiRequest<{ id: number }>(`/chat-models/${id}`, { method: 'DELETE' })
}

export function setDefaultChatModel(id: number): Promise<ChatModel> {
  return apiRequest<ChatModel>(`/chat-models/${id}/default`, { method: 'POST' })
}

/**
 * 连通性测试：后端真发一条请求给模型。
 *
 * 超时给到 45s 而不是默认的 15s：推理型模型在冷启动时经常要十几秒，
 * 用普通超时会让"能用"被误判成"超时"。
 */
export function testChatModel(id: number): Promise<ChatModelProbe> {
  return apiRequest<ChatModelProbe>(`/chat-models/${id}/test`, { method: 'POST', timeoutMs: 45_000 })
}
