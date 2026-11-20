import { streamSSE, type AskHandlers } from '@/api/ask'
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

/** 审批后从 checkpoint 恢复，delta 与普通问答同形，直接续到会话里。 */
export function streamApprovalDecision(
  id: number,
  decision: 'approve' | 'reject',
  handlers: AskHandlers,
  signal?: AbortSignal,
): Promise<void> {
  return streamSSE(
    '/approvals/' + id + '/decision',
    { decision },
    handlers,
    signal,
  )
}
