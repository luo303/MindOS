import { AlertTriangle, StopCircle } from 'lucide-react'
import { Link } from 'react-router'

import type { AskCitationItem, AskRetrievalItem } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

import {
  knowledgeHref,
  sectionLabel,
  similarityLabel,
  type Turn,
} from './format'
import { MarkdownAnswer } from './markdown-answer'
import { useTypewriter } from './use-typewriter'

type Props = {
  turn: Turn
  onStop: () => void
}

export function TurnCard({ turn, onStop }: Props) {
  const streaming = turn.phase === 'streaming'

  return (
    <article className="glass-surface space-y-4 p-4 sm:p-5">
      <h2 className="ml-auto w-fit max-w-[90%] rounded-xl bg-accent px-3.5 py-2.5 text-sm leading-relaxed font-medium break-words whitespace-pre-wrap">
        {turn.question}
      </h2>
      <div className="space-y-3 break-words">
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
          {turn.done ? (
            <details className="text-muted-foreground text-xs">
              <summary className="w-fit cursor-pointer py-1">运行信息</summary>
              <p className="pt-1">
                {(turn.done.latency_ms / 1000).toFixed(1)}s · 输入 {turn.done.prompt_tokens} / 输出{' '}
                {turn.done.completion_tokens} tokens
                {turn.done.run_id > 0 ? ` · #${turn.done.run_id}` : ''}
              </p>
            </details>
          ) : null}
        </div>
      </div>
    </article>
  )
}

function AnswerBody({ turn }: { turn: Turn }) {
  // 把逐字更新限制在回答正文，避免每个字都重渲染整张 TurnCard。
  const answer = useTypewriter(turn.answer, turn.phase === 'streaming')
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
        {answer ? <p className="text-sm whitespace-pre-wrap">{answer}</p> : null}
      </div>
    )
  }

  if (turn.phase === 'refused' || turn.phase === 'withdrawn') {
    // 拒答与撤回共用灰底样式：对用户都是"没答上来"，区别只在文案。
    return (
      <div className="bg-muted/50 rounded-md border p-3">
        <p className="text-sm whitespace-pre-wrap">{answer}</p>
        {turn.reason ? (
          <p className="text-muted-foreground mt-1 text-xs">原因：{reasonLabel(turn.reason)}</p>
        ) : null}
      </div>
    )
  }

  return <MarkdownAnswer text={answer} citations={turn.citations} />
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
