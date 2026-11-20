import type { Approval } from '@/api/approvals'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

type Props = {
  approval: Approval
  deciding: boolean
  error: string
  onDecision: (decision: 'approve' | 'reject') => void
}

/**
 * 审批面板与输入框同一层级：有待审批时直接占据底部输入区，
 * 不允许继续对话。审批完成后面板卸载，输入框恢复。
 */
export function ApprovalPanel({ approval, deciding, error, onDecision }: Props) {
  return (
    <section
      aria-label="待审批操作"
      className="border-primary/30 bg-primary/5 space-y-3 rounded-xl border p-3"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Badge>待审批</Badge>
        <p className="text-sm font-medium">{approval.summary}</p>
      </div>
      <p className="text-muted-foreground text-xs">
        AI 写操作 · {approval.tool_name} · 批准后模型会从断点继续
      </p>

      {typeof approval.arguments.title === 'string' ? (
        <p className="text-sm">标题：{approval.arguments.title}</p>
      ) : null}
      {typeof approval.arguments.due_at === 'string' ? (
        <p className="text-muted-foreground text-xs">
          截止：{new Date(approval.arguments.due_at).toLocaleString('zh-CN')}
        </p>
      ) : null}

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      <div className="flex gap-2">
        <Button size="sm" onClick={() => onDecision('approve')} disabled={deciding}>
          {deciding ? '处理中…' : '批准'}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => onDecision('reject')} disabled={deciding}>
          拒绝
        </Button>
      </div>
    </section>
  )
}
