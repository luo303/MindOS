import { apiRequest } from '@/api/client'

export type PreferenceKey = 'answer_language' | 'answer_detail' | 'code_example_language'

export type MemoryMetadata = { category: string; scope: string; expires_at: string | null; pinned: boolean }
export type Preference = MemoryMetadata & {
  id: number
  key: string
  value: string
  status: 'active' | 'archived'
  version: number
}

export type MemoryVersion = MemoryMetadata & {
  version: number
  value: string
  status: Preference['status']
  source: 'chat' | 'manual'
  message_id?: number
  conversation_id?: number
  created_at: string
  automatic: boolean
}

export type MemoryCandidate = MemoryMetadata & {
  id: number
  key: string
  action: string
  target_id?: number
  value: string
  message_id: number
  conversation_id: number
  evidence: string
  base_version: number
}

export type MemorySettings = { mode: 'auto' | 'suggest' | 'off'; conversation_off: boolean }
export type MemoryEvidence = { version: number; message_id: number; conversation_id: number; excerpt: string }

export const memoryKeys = {
  preferences: ['memories'] as const,
  versions: (id: number) => ['memories', id, 'versions'] as const,
  candidates: ['memory-candidates'] as const,
  conversation: (id: number) => ['memory-candidates', id] as const,
  settings: (id?: number) => ['memory-settings', id ?? 0] as const,
  evidence: (id: number) => ['memories', id, 'evidence'] as const,
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

export function createMemory(value: string, metadata: MemoryMetadata) {
  return apiRequest<Preference>('/memories', { method: 'POST', body: { value, ...metadata } })
}

export function updatePreference(item: Preference, patch: { value?: string; status?: Preference['status']; metadata?: MemoryMetadata }) {
  return apiRequest<Preference>(`/memories/${item.id}`, {
    method: 'PATCH', body: { expected_version: item.version, ...patch },
  })
}

export function fetchMemorySettings(signal?: AbortSignal, id?: number) {
  return apiRequest<MemorySettings>(`/memory-settings${id ? `?conversation_id=${id}` : ''}`, { signal })
}

export function updateMemorySettings(patch: { mode?: MemorySettings['mode']; conversation_id?: number; conversation_off?: boolean }) {
  return apiRequest<MemorySettings>('/memory-settings', { method: 'PATCH', body: patch })
}

export async function fetchMemoryEvidence(id: number, signal?: AbortSignal) {
  return (await apiRequest<{ items: MemoryEvidence[] }>(`/memories/${id}/evidence`, { signal })).items
}

export function decideMemoryCandidate(id: number, decision: 'accept' | 'reject', value?: string) {
  return apiRequest(`/memory-candidates/${id}/decision`, { method: 'POST', body: { decision, value } })
}
