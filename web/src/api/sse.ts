import { wails } from './wails'

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
  const w = wails()
  if (w) return connectWails(w, o)
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

const EVENTS_URL = '/v1/events'
const LOGS_URL = /^\/v1\/repos\/([^/]+)\/boxes\/([^/]+)\/logs(\?|$)/
let logSeq = 0 // part of each client-chosen log stream id

// In the desktop app the asset server cannot stream, so the Go side pumps the
// daemon's streams into Wails events (spec phase 5 §1.1, R4).
function connectWails(w: NonNullable<ReturnType<typeof wails>>, o: SSEOptions): () => void {
  o.onState?.('connecting')
  if (o.url === EVENTS_URL) {
    let sawConn = false // a live conn event is fresher than the ConnState() snapshot
    let needReload = true // until we know we were live before anything could be missed
    const offEvent = w.runtime.EventsOn('agentbox:event', (data) => {
      const type = (data as { type?: string })?.type
      if (type && o.events.includes(type)) o.onEvent(type, data)
    })
    const offConn = w.runtime.EventsOn('agentbox:conn', (state) => {
      sawConn = true
      if (state === 'live') {
        o.onState?.('open')
        if (needReload) o.onReconnect?.()
        needReload = true
      } else {
        o.onState?.('reconnecting')
        needReload = true // anything may be missed while down
      }
    })
    void w.app
      .ConnState()
      .then((state) => {
        if (sawConn) return
        if (state === 'live') {
          o.onState?.('open')
          needReload = false // we are current; the next 'live' is a real reconnect
        } else {
          o.onState?.('reconnecting')
        }
      })
      .catch(() => {
        if (!sawConn) o.onState?.('reconnecting')
      })
    return () => {
      offEvent()
      offConn()
    }
  }
  const m = LOGS_URL.exec(o.url)
  if (!m) {
    o.onState?.('reconnecting') // nothing to stream here; show it as not live rather than crash
    return () => {}
  }
  const repo = decodeURIComponent(m[1])
  const name = decodeURIComponent(m[2])
  let closed = false
  let id: string | null = null
  let off: (() => void) | null = null
  let offEnd: (() => void) | null = null
  let attempt = 0
  let everOpened = false
  let timer: ReturnType<typeof setTimeout> | undefined

  const unsubscribe = () => {
    off?.()
    offEnd?.()
    off = offEnd = null
  }
  // The stream ends when its source does (the box was removed, the daemon is
  // restarting); like the browser branch, come back with 1s→16s backoff.
  const retry = () => {
    if (closed) return
    const delay = Math.min(16_000, 1_000 * 2 ** attempt)
    attempt++
    o.onState?.('reconnecting')
    timer = setTimeout(start, delay)
  }
  const start = () => {
    if (closed) return
    // The window picks the id and listens BEFORE asking Go to stream: the
    // bridge's first lines (or a quick :end) would otherwise race the
    // StartLogs reply across Wails' unordered main-thread calls and be lost.
    const streamId = `${repo}/${name}#${++logSeq}-${Math.random().toString(36).slice(2, 8)}`
    let ended = false
    off = w.runtime.EventsOn('agentbox:log:' + streamId, (line) => o.onEvent('line', line))
    // EventsOnce: the runtime removes it itself; a listener that called its
    // own canceller from inside the callback would be put back by Wails.
    offEnd = w.runtime.EventsOnce('agentbox:log:' + streamId + ':end', () => {
      ended = true
      unsubscribe()
      id = null // the bridge already forgot this stream
      retry()
    })
    w.app.StartLogs(streamId, repo, name).then(
      () => {
        if (closed) {
          void w.app.StopLogs(streamId)
          return
        }
        if (ended) return // it ended before the reply arrived; the retry is scheduled
        id = streamId
        attempt = 0
        const was = everOpened
        everOpened = true
        o.onState?.('open')
        if (was) o.onReconnect?.()
      },
      () => {
        unsubscribe()
        retry()
      },
    )
  }
  start()
  return () => {
    closed = true
    clearTimeout(timer)
    unsubscribe()
    if (id) void w.app.StopLogs(id)
  }
}
