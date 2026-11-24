import { Loader2 } from 'lucide-react'
import { useId } from 'react'

import { describeError } from '@/api/errors'
import { Button } from '@/components/ui/button'
import { Modal } from '@/components/ui/modal'

type Props = {
  title: string
  targetName: string
  consequence?: string
  pending: boolean
  error: Error | null
  onCancel: () => void
  onConfirm: () => void
}

export function DeleteConfirmDialog({ title, targetName, consequence, pending, error, onCancel, onConfirm }: Props) {
  const descriptionId = useId()

  return (
    <Modal open title={title} descriptionId={descriptionId} onClose={onCancel} dismissible={!pending} className="max-w-sm">
      <div id={descriptionId} className="text-muted-foreground space-y-1 text-sm break-words">
        <p>确定删除「<span className="text-foreground font-medium">{targetName}</span>」？</p>
        <p>{consequence ?? '此操作无法撤销。'}</p>
      </div>
      {error ? (
        <p role="alert" className="text-destructive mt-3 text-sm break-words">{describeError(error)}</p>
      ) : null}
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="sm" data-initial-focus onClick={onCancel} disabled={pending}>取消</Button>
        <Button variant="destructive" size="sm" onClick={onConfirm} disabled={pending}>
          {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          {pending ? '删除中…' : '确认删除'}
        </Button>
      </div>
    </Modal>
  )
}
