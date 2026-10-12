export type SSEState = 'connecting' | 'open' | 'reconnecting'

export interface SSEOptions {
  url: string
  events: string[]
  onEvent: (type: string, data: unknown) => void
  onState?: (s: SSEState) => void
  /** Called after a dropped stream comes back; callers reload what they may have missed. */
  onReconnect?: () => void
}

/** Opens an EventSource that reconnects with 1s→16s backoff. Returns a close function. */
export function connectSSE(o: SSEOptions): () => void {
  let es: EventSource | null = null
  let closed = false
  let attempt = 0
  let everOpened = false
  let timer: ReturnType<typeof setTimeout> | undefined

  const open = () => {
    if (closed) return
    o.onState?.(attempt === 0 ? 'connecting' : 'reconnecting')
    es = new EventSource(o.url)
    es.onopen = () => {
      const was = everOpened
      everOpened = true
      attempt = 0
      o.onState?.('open')
      if (was) o.onReconnect?.()
    }
    for (const t of o.events) {
      es.addEventListener(t, (ev) => {
        try {
          o.onEvent(t, JSON.parse((ev as MessageEvent).data))
        } catch {
          /* malformed frame: ignore */
        }
      })
    }
    es.onerror = () => {
      es?.close()
      es = null
      if (closed) return
      const delay = Math.min(16_000, 1_000 * 2 ** attempt)
      attempt++
      o.onState?.('reconnecting')
      timer = setTimeout(open, delay)
    }
  }
  open()
  return () => {
    closed = true
    clearTimeout(timer)
    es?.close()
  }
}
