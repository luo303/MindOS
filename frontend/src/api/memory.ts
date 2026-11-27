import { apiRequest } from '@/api/client'

export type PreferenceKey = 'answer_language' | 'answer_detail' | 'code_example_language'

export type Preference = {
  id: number
  key: PreferenceKey
  value: string
  status: 'active' | 'archived'
  version: number
}

export type MemoryVersion = {
  version: number
  value: string
  status: Preference['status']
  source: 'chat' | 'manual'
  message_id?: number
  conversation_id?: number
  created_at: string
}

export type MemoryCandidate = {
  id: number
  key: PreferenceKey
  value: string
  message_id: number
  conversation_id: number
  evidence: string
  base_version: number
}

export const memoryKeys = {
  preferences: ['memories'] as const,
  versions: (id: number) => ['memories', id, 'versions'] as const,
  candidates: ['memory-candidates'] as const,
  conversation: (id: number) => ['memory-candidates', id] as const,
}

export async function fetchPreferences(signal?: AbortSignal) {
  return (await apiRequest<{ items: Preference[] }>('/memories', { signal })).items
}

export async function fetchMemoryCandidates(signal?: AbortSignal, conversationId?: number) {
  const filter = conversationId === undefined ? '' : `?conversation_id=${conversationId}`
  return (await apiRequest<{ items: MemoryCandidate[] }>(`/memory-candidates${filter}`, { signal })).items
}

export async function fetchMemoryVersions(id: number, before: number, signal?: AbortSignal) {
  const filter = before === 0 ? '' : `?before_version=${before}`
  return (await apiRequest<{ items: MemoryVersion[] }>(`/memories/${id}/versions${filter}`, { signal })).items
}

export function createPreference(key: PreferenceKey, value: string) {
  return apiRequest<Preference>('/memories', { method: 'POST', body: { key, value } })
}

export function updatePreference(item: Preference, patch: { value?: string; status?: Preference['status'] }) {
  return apiRequest<Preference>(`/memories/${item.id}`, {
    method: 'PATCH', body: { expected_version: item.version, ...patch },
  })
}

export function decideMemoryCandidate(id: number, decision: 'accept' | 'reject', value?: string) {
  return apiRequest(`/memory-candidates/${id}/decision`, { method: 'POST', body: { decision, value } })
}
