import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { connectSSE, type SSEState } from '../api/sse'

const MAX_LINES = 5_000

export function LogView({ repo, name }: { repo: string; name: string }) {
  const [lines, setLines] = useState<string[]>([])
  const [conn, setConn] = useState<SSEState>('connecting')
  const [follow, setFollow] = useState(true)
  const pre = useRef<HTMLPreElement>(null)

  useEffect(() => {
    setLines([])
    return connectSSE({
      url: api.logsURL(repo, name, 500),
      events: ['line'],
      onState: (s) => {
        setConn(s)
        if (s === 'reconnecting') setLines([])
      },
      onEvent: (_t, data) =>
        setLines((ls) => {
          const next = ls.length >= MAX_LINES ? ls.slice(ls.length - MAX_LINES + 1) : ls.slice()
          next.push(String(data))
          return next
        }),
    })
  }, [repo, name])

  useEffect(() => {
    if (follow && pre.current) pre.current.scrollTop = pre.current.scrollHeight
  }, [lines, follow])

  const onScroll = () => {
    const el = pre.current
    if (!el) return
    setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 8)
  }

  return (
    <div className="logs">
      <div className="row">
        <span className="muted">
          {lines.length} lines · {conn === 'open' ? 'streaming' : conn}
        </span>
        <span className="spacer" />
        {!follow && <button onClick={() => setFollow(true)}>Follow</button>}
        <button onClick={() => setLines([])}>Clear</button>
      </div>
      <pre ref={pre} onScroll={onScroll} className="log">
        {lines.join('\n')}
      </pre>
    </div>
  )
}
