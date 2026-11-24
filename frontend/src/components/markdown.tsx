import ReactMarkdown, { type Components } from 'react-markdown'
import { Link } from 'react-router'
import remarkGfm from 'remark-gfm'

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

/** 通用 Markdown 渲染；HTML 不走 rehype-raw，保持转义。 */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={className ?? 'text-sm leading-relaxed [&>*:first-child]:mt-0 [&>*:last-child]:mb-0'}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  )
}
