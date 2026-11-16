import { AlertTriangle, Link2, StopCircle } from 'lucide-react'
import { Link } from 'react-router'

import type { AskCitationItem, AskRetrievalItem } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

import {
  knowledgeHref,
  sectionLabel,
  similarityLabel,
  type Turn,
} from './format'

type Props = {
  turn: Turn
  onRetry: () => void
  onStop: () => void
}

export function TurnCard({ turn, onRetry, onStop }: Props) {
  const streaming = turn.phase === 'streaming'

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm leading-relaxed font-medium">{turn.question}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <AnswerBody turn={turn} />

        {turn.citations.length > 0 ? <CitationList items={turn.citations} /> : null}
        {turn.hits.length > 0 ? <RetrievalPanel items={turn.hits} /> : null}

        <div className="flex flex-wrap items-center gap-2">
          {streaming ? (
            <Button variant="outline" size="sm" onClick={onStop}>
              <StopCircle aria-hidden="true" />
              停止
            </Button>
          ) : null}
          {turn.phase === 'failed' ? (
            <Button variant="outline" size="sm" onClick={onRetry}>
              重试
            </Button>
          ) : null}
          {turn.done ? (
            <p className="text-muted-foreground text-xs">
              {(turn.done.latency_ms / 1000).toFixed(1)}s · 输入 {turn.done.prompt_tokens} / 输出{' '}
              {turn.done.completion_tokens} tokens
              {turn.done.run_id > 0 ? ` · #${turn.done.run_id}` : ''}
            </p>
          ) : null}
        </div>
      </CardContent>
    </Card>
  )
}

function AnswerBody({ turn }: { turn: Turn }) {
  // 首字之前显示骨架：空白区域会让人以为"没点上"，而这段时间可能要几秒。
  if (turn.phase === 'streaming' && turn.answer === '') {
    return (
      <div className="space-y-2" aria-busy="true" aria-label="正在生成回答">
        <Skeleton className="h-4 w-4/5" />
        <Skeleton className="h-4 w-3/5" />
      </div>
    )
  }

  if (turn.phase === 'failed') {
    return (
      <div role="alert" className="border-destructive/30 bg-destructive/5 space-y-1 rounded-md border p-3">
        <p className="text-destructive flex items-center gap-1.5 text-sm font-medium">
          <AlertTriangle className="size-4" aria-hidden="true" />
          没有拿到回答
        </p>
        <p className="text-muted-foreground text-xs break-words">{turn.error?.message}</p>
        {turn.answer ? <p className="text-sm whitespace-pre-wrap">{turn.answer}</p> : null}
      </div>
    )
  }

  if (turn.phase === 'refused' || turn.phase === 'withdrawn') {
    // 拒答与撤回共用灰底样式：对用户都是"没答上来"，区别只在文案。
    return (
      <div className="bg-muted/50 rounded-md border p-3">
        <p className="text-sm whitespace-pre-wrap">{turn.answer}</p>
        {turn.reason ? (
          <p className="text-muted-foreground mt-1 text-xs">原因：{reasonLabel(turn.reason)}</p>
        ) : null}
      </div>
    )
  }

  return (
    <p className="text-sm leading-relaxed whitespace-pre-wrap">
      {renderAnswer(turn.answer, turn.citations)}
    </p>
  )
}

/**
 * 把正文里的 [n] 渲染成可点 chip。
 *
 * 只有 citation 事件到过之后才有 mapping，所以 streaming 期间 [n] 仍是纯文本——
 * 这也正好符合"裁决之前没有任何引用是可信的"这条规则。
 */
function renderAnswer(text: string, citations: AskCitationItem[]) {
  if (citations.length === 0) {
    return text
  }
  const byNumber = new Map(citations.map((item) => [item.n, item]))
  // 用捕获组切分，split 会把分隔符本身也保留在结果里。
  const parts = text.split(/(\[\d{1,3}\])/g)
  return parts.map((part, index) => {
    const matched = /^\[(\d{1,3})\]$/.exec(part)
    if (!matched) {
      return <span key={index}>{part}</span>
    }
    const n = Number(matched[1])
    const citation = byNumber.get(n)
    if (!citation) {
      return <span key={index}>{part}</span>
    }
    return (
      <Link
        key={index}
        to={knowledgeHref(citation.document_id, citation.seq)}
        className="text-primary hover:bg-primary/10 mx-0.5 rounded px-1 text-xs font-medium no-underline"
        title={citation.title}
      >
        [{n}]
      </Link>
    )
  })
}

function CitationList({ items }: { items: AskCitationItem[] }) {
  return (
    <div className="space-y-1.5 border-t pt-3">
      <p className="text-muted-foreground text-xs">引用来源</p>
      <ul className="grid gap-1.5 sm:grid-cols-2">
        {items.map((item) => (
          <li key={item.chunk_id}>
            <Link
              to={knowledgeHref(item.document_id, item.seq)}
              className="hover:bg-muted/60 flex items-start gap-2 rounded-md border p-2 no-underline transition-colors"
            >
              <span className="text-primary text-xs font-medium">[{item.n}]</span>
              <span className="min-w-0 space-y-0.5">
                <span className="block truncate text-xs font-medium">{item.title}</span>
                <span className="text-muted-foreground block text-xs">
                  {sectionLabel(item.seq)} · {similarityLabel(item.similarity)}
                </span>
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  )
}

function RetrievalPanel({ items }: { items: AskRetrievalItem[] }) {
  return (
    <details className="group border-t pt-3">
      <summary className="text-muted-foreground cursor-pointer text-xs select-none">
        本次依据（{items.length} 条）
      </summary>
      <ul className="mt-2 space-y-2">
        {items.map((item) => (
          <li key={item.chunk_id} className="bg-muted/30 rounded-md border p-2">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="outline">[{item.n}]</Badge>
              <span className="truncate text-xs font-medium">{item.title}</span>
              <span className="text-muted-foreground text-xs">
                {sectionLabel(item.seq)} · {similarityLabel(item.similarity)}
              </span>
            </div>
            <p className="text-muted-foreground mt-1 line-clamp-2 text-xs break-words">{item.snippet}</p>
          </li>
        ))}
      </ul>
    </details>
  )
}

/**
 * 裁决原因的人类可读版。
 *
 * 不直接把后端的 reason 文案显示出来：那些是给机器看的稳定标识，
 * 直接把 no_evidence 展示给用户等于让他去读代码。
 */
function reasonLabel(reason: string): string {
  switch (reason) {
    case 'no_evidence':
      return '检索到的内容与问题无关'
    case 'no_citation':
      return '回答没有标注来源'
    case 'unknown_citation':
      return '回答引用了不存在的来源'
    case 'model_refused':
      return '模型判断资料不足'
    default:
      return reason
  }
}

/** 空态：还没有问过任何问题。 */
export function ChatEmptyState() {
  return (
    <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed py-12 text-center">
      <Link2 className="text-muted-foreground size-6" aria-hidden="true" />
      <p className="text-sm font-medium">问一句关于你知识库里内容的话</p>
      <p className="text-muted-foreground max-w-md text-xs">
        回答只使用已导入的文档，并在每句后面标注来源编号。库里没有依据时会直接说没有，
        不会凭空编造。
      </p>
    </div>
  )
}
