import { FileText, Loader2 } from 'lucide-react'
import { useRef, useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { formatBytes, splitTags } from './format'

/** 与后端 normalizeMetadata 的上限一致，前端先挡住，避免一次往返才发现。 */
const MAX_TAGS = 20
const MAX_TAG_LENGTH = 32

export type UploadDraft = {
  collection: string
  tags: string[]
}

/**
 * 文件导入的确认面板。
 *
 * 存在的理由：选中文件就直接上传，用户没有任何机会填分类和标签——
 * 笔记表单能填、详情页能改、唯独文件导入不行，同一个功能在不同入口能力不一致。
 * 所以这里把"选文件"和"确认导入"拆成两步，中间留给元信息。
 *
 * 由面板内的按钮打开原生选择器；选中的文件交给页面，确认时再连同元信息上传。
 */
export function UploadComposer({
  file,
  saving,
  onSelectFile,
  onSubmit,
  onCancel,
}: {
  file: File | null
  saving: boolean
  onSelectFile: (file: File) => void
  onSubmit: (draft: UploadDraft) => void
  onCancel: () => void
}) {
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [collection, setCollection] = useState('')
  const [tags, setTags] = useState('')

  const parsedTags = splitTags(tags)
  // 只提示、不禁用：用户可能正在输入第 21 个标签的途中，
  // 立刻禁用提交会让人以为界面卡住了。
  const tagProblem =
    parsedTags.length > MAX_TAGS
      ? `最多 ${MAX_TAGS} 个标签，当前 ${parsedTags.length} 个`
      : parsedTags.find((tag) => Array.from(tag).length > MAX_TAG_LENGTH)
        ? `单个标签最多 ${MAX_TAG_LENGTH} 个字`
        : ''

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (file === null || tagProblem !== '') {
      return
    }
    onSubmit({ collection: collection.trim(), tags: parsedTags })
  }

  return (
    <form className="space-y-3" onSubmit={submit}>
          <input
            ref={fileInputRef}
            type="file"
            accept=".md,.markdown,.txt,.text"
            className="hidden"
            aria-label="选择要导入的文件"
            onChange={(event) => {
              const selected = event.currentTarget.files?.[0]
              event.currentTarget.value = ''
              if (selected) onSelectFile(selected)
            }}
          />
          <div className="border-border flex items-center justify-between gap-3 rounded-lg border px-3 py-2">
            {file ? (
              <div className="flex min-w-0 items-center gap-2">
                <FileText className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
                <span className="truncate text-sm" title={file.name}>
                  {file.name}
                </span>
                <span className="text-muted-foreground shrink-0 text-xs">{formatBytes(file.size)}</span>
              </div>
            ) : (
              <p className="text-muted-foreground text-sm">Markdown / 纯文本</p>
            )}
            <Button type="button" variant="outline" size="sm" onClick={() => fileInputRef.current?.click()} disabled={saving}>
              {file ? '重新选择' : '选择文件'}
            </Button>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <Input
              value={collection}
              onChange={(e) => setCollection(e.target.value)}
              placeholder="分类（可选）"
              aria-label="文件分类"
              maxLength={64}
            />
            <Input
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="标签（逗号分隔）"
              aria-label="文件标签"
            />
          </div>
          {tagProblem !== '' ? (
            <p className="text-destructive text-xs" role="alert">{tagProblem}</p>
          ) : null}

          <div className="flex items-center justify-end gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
              取消
            </Button>
            <Button type="submit" size="sm" disabled={saving || file === null || tagProblem !== ''}>
              {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
              {saving ? '导入中' : '开始导入'}
            </Button>
          </div>
    </form>
  )
}
