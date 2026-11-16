import { apiRequest } from '@/api/client'
import type { SettingsView, SystemStatus } from '@/api/types'

/**
 * 查询键集中登记。
 *
 * TanStack Query 靠"键"判断缓存归属，键散落在各组件里时，
 * 一处写 ['system','status'] 另一处写 ['system-status'] 就会各存一份、互不刷新。
 */
export const queryKeys = {
  systemStatus: ['system', 'status'] as const,
  settings: ['settings'] as const,
}

export function fetchSystemStatus(signal?: AbortSignal): Promise<SystemStatus> {
  return apiRequest<SystemStatus>('/system/status', { signal })
}

export function fetchSettings(signal?: AbortSignal): Promise<SettingsView> {
  return apiRequest<SettingsView>('/settings', { signal })
}

/** 向量模型连通性测试的结果。 */
export type EmbeddingTestResult = {
  ok: boolean
  model: string
  dimensions: number
  latency_ms: number
}

/**
 * 真调一次向量模型：POST /settings/embedding/test。
 *
 * 为什么不在 /system/status 里顺手做：状态接口需要快、可频繁轮询，
 * 而这里每次都会真的打一次外部模型（有成本、有延迟），所以单独成接口。
 * 超时给 30s：本机 CPU 推理一条短文本可能就要几秒，模型冷启动更久。
 */
export function testEmbedding(): Promise<EmbeddingTestResult> {
  return apiRequest<EmbeddingTestResult>('/settings/embedding/test', {
    method: 'POST',
    timeoutMs: 30_000,
  })
}
