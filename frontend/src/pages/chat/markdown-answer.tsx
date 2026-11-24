import type { AskCitationItem } from '@/api/types'
import { Markdown } from '@/components/markdown'

import { knowledgeHref } from './format'

/**
 * 把回答里的 [n] 换成指向知识库的 Markdown 链。
 *
 * 先匹配 fenced code / inline code，避免把代码里的 [1] 也变成引用。
 * 未知编号保持纯文本；流式期间 citation 事件还没到时也保持纯文本。
 */
function withCitationLinks(text: string, citations: AskCitationItem[]): string {
  if (citations.length === 0) {
    return text
  }
  const byNumber = new Map(citations.map((item) => [item.n, item]))
  const pattern = /```[\s\S]*?```|`[^`\n]*`|\[(\d{1,3})\](?!\()/g
  return text.replace(pattern, (match, n?: string) => {
    if (!n) {
      return match
    }
    const citation = byNumber.get(Number(n))
    if (!citation) {
      return match
    }
    // 链接文本用 \[n\] 转义，最终仍显示成 [1]，而不是去掉方括号的 1。
    return '[\\[' + n + '\\]](' + knowledgeHref(citation.document_id, citation.seq) + ')'
  })
}

/** 回答正文的 Markdown 渲染。 */
export function MarkdownAnswer({ text, citations }: { text: string; citations: AskCitationItem[] }) {
  return <Markdown text={withCitationLinks(text, citations)} />
}
