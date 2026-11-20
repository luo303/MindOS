import ReactMarkdown, { type Components } from 'react-markdown'
import { Link } from 'react-router'
import remarkGfm from 'remark-gfm'

import type { AskCitationItem } from '@/api/types'

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

const components: Components = {
  p: ({ children }) => <p className="mb-3 leading-relaxed last:mb-0">{children}</p>,
  h1: ({ children }) => <h1 className="mt-5 mb-2 text-lg font-semibold first:mt-0">{children}</h1>,
  h2: ({ children }) => <h2 className="mt-4 mb-2 text-base font-semibold first:mt-0">{children}</h2>,
  h3: ({ children }) => <h3 className="mt-3 mb-1.5 text-sm font-semibold first:mt-0">{children}</h3>,
  ul: ({ children }) => <ul className="mb-3 list-disc space-y-1 pl-5">{children}</ul>,
  ol: ({ children }) => <ol className="mb-3 list-decimal space-y-1 pl-5">{children}</ol>,
  li: ({ children }) => <li className="pl-0.5">{children}</li>,
  blockquote: ({ children }) => (
    <blockquote className="border-border text-muted-foreground mb-3 border-l-2 pl-3">
      {children}
    </blockquote>
  ),
  hr: () => <hr className="border-border my-4" />,
  strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
  a: ({ href, children }) => {
    if (!href) {
      return <span>{children}</span>
    }
    if (href.startsWith('/')) {
      return (
        <Link to={href} className="text-primary hover:bg-primary/10 rounded px-1 text-xs font-medium no-underline">
          {children}
        </Link>
      )
    }
    return (
      <a
        href={href}
        target="_blank"
        rel="noreferrer"
        className="text-primary underline underline-offset-2"
      >
        {children}
      </a>
    )
  },
  code: ({ className, children }) => {
    const block = className?.includes('language-')
    if (block) {
      return <code className="text-xs">{children}</code>
    }
    return (
      <code className="bg-muted rounded px-1 py-0.5 font-mono text-[0.85em]">{children}</code>
    )
  },
  pre: ({ children }) => (
    <pre className="bg-muted mb-3 overflow-x-auto rounded-md p-3 font-mono text-xs leading-relaxed">
      {children}
    </pre>
  ),
  table: ({ children }) => (
    <div className="mb-3 overflow-x-auto">
      <table className="w-full border-collapse text-xs">{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th className="border-border bg-muted/60 border px-2 py-1.5 text-left font-medium">{children}</th>
  ),
  td: ({ children }) => <td className="border-border border px-2 py-1.5">{children}</td>,
}

/** 回答正文的 Markdown 渲染；HTML 不走 rehype-raw，保持转义。 */
export function MarkdownAnswer({ text, citations }: { text: string; citations: AskCitationItem[] }) {
  return (
    <div className="text-sm leading-relaxed [&>*:first-child]:mt-0 [&>*:last-child]:mb-0">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {withCitationLinks(text, citations)}
      </ReactMarkdown>
    </div>
  )
}
