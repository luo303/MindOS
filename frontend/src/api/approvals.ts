import { apiRequest } from '@/api/client'

export type ApprovalStatus = 'pending' | 'approved' | 'rejected' | 'completed' | 'expired' | 'failed'

export type Approval = {
  id: number
  conversation_id?: number | null
  user_message_id?: number | null
  chat_model_id?: number | null
  tool_name: string
  arguments: Record<string, unknown>
  summary: string
  status: ApprovalStatus
  expires_at: string
  decided_at?: string | null
  result: Record<string, unknown>
  created_at: string
  updated_at: string
}

export const approvalKeys = {
  conversation: (conversationId: number) => ['approvals', 'conversation', conversationId] as const,
}

export async function fetchConversationApprovals(
  conversationId: number,
  signal?: AbortSignal,
): Promise<Approval[]> {
  const data = await apiRequest<{ items: Approval[] | null }>(
    '/approvals?conversation_id=' + conversationId + '&status=all',
    { signal },
  )
  return data.items ?? []
}

export function decideApproval(id: number, decision: 'approve' | 'reject'): Promise<Approval> {
  return apiRequest<Approval>('/approvals/' + id + '/decision', {
    method: 'POST',
    body: { decision },
    timeoutMs: 60_000,
  })
}
