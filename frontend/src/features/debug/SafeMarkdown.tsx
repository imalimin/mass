import { useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
export default function SafeMarkdown({ text }: { text: string }) {
  const [raw, setRaw] = useState(false)
  return <div className="safe-markdown"><button className="secondary" onClick={() => setRaw(v => !v)}>{raw ? '格式化显示' : '查看原文'}</button>{raw ? <pre>{text}</pre> : <Markdown remarkPlugins={[remarkGfm]} skipHtml components={{ img: () => null, a: ({ href, children }) => href && /^https?:\/\//i.test(href) ? <a href={href} target="_blank" rel="noreferrer">{children}</a> : <span>{children}</span> }}>{text}</Markdown>}</div>
}
