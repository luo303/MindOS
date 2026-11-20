import { X } from 'lucide-react'

import type { Approval } from '@/api/approvals'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

type Props = {
  open: boolean
  approvals: Approval[]
  decidingId: number | null
  streamText: string
  error: string
  onClose: () => void
  onDecision: (id: number, decision: 'approve' | 'reject') => void
}

const statusLabel: Record<string, string> = {
  pending: '待审批',
  approved: '已批准',
  rejected: '已拒绝',
  completed: '已处理',
  expired: '已过期',
  failed: '失败',
}

/**
 * 审批弹框：挂在对话页上，但审批记录仍属于当前会话。
 * 自动打开由 ChatPage 根据 pending 数量控制；关闭后可从“审批”按钮重开。
 */
export function ApprovalDialog({
  open,
  approvals,
  decidingId,
  streamText,
  error,
  onClose,
  onDecision,
}: Props) {
  if (!open) {
    return null
  }
  const pending = approvals.filter((item) => item.status === 'pending')
  const history = approvals.filter((item) => item.status !== 'pending')

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose()
        }
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="审批"
        className="bg-background w-full max-w-xl rounded-lg border p-4 shadow-xl"
      >
        <div className="flex items-center justify-between gap-3 border-b pb-3">
          <div>
            <h2 className="text-base font-semibold">审批</h2>
            <p className="text-muted-foreground text-xs">
              AI 写操作需要你确认；批准后模型会从断点继续。
            </p>
          </div>
          <Button variant="ghost" size="icon" aria-label="关闭审批" onClick={onClose}>
            <X aria-hidden="true" />
          </Button>
        </div>

        <div className="max-h-[60vh] space-y-3 overflow-y-auto py-3">
          {approvals.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-sm">当前会话没有审批记录</p>
          ) : null}

          {pending.map((item) => (
            <div key={item.id} className="border-primary/30 bg-primary/5 space-y-2 rounded-md border p-3">
              <div className="flex flex-wrap items-center gap-2">
                <Badge>待审批</Badge>
                <span className="text-sm font-medium">{item.summary}</span>
              </div>
              <p className="text-muted-foreground text-xs">
                {item.tool_name} · {new Date(item.created_at).toLocaleString('zh-CN')}
              </p>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  onClick={() => onDecision(item.id, 'approve')}
                  disabled={decidingId === item.id}
                >
                  {decidingId === item.id ? '处理中…' : '批准'}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onDecision(item.id, 'reject')}
                  disabled={decidingId === item.id}
                >
                  拒绝
                </Button>
              </div>
            </div>
          ))}

          {streamText ? (
            <div className="bg-muted/50 rounded-md border p-3 text-sm whitespace-pre-wrap">{streamText}</div>
          ) : null}
          {error ? <p className="text-destructive text-sm">{error}</p> : null}

          {history.length > 0 ? (
            <div className="space-y-2 border-t pt-3">
              <p className="text-muted-foreground text-xs">审批记录</p>
              {history.map((item) => (
                <div key={item.id} className="flex items-center justify-between gap-2 text-xs">
                  <span className="truncate">{item.summary}</span>
                  <Badge variant="secondary">{statusLabel[item.status] ?? item.status}</Badge>
                </div>
              ))}
            </div>
          ) : null}
        </div>
      </div>
    </div>
  )
}
