import { afterEach, describe, expect, it, vi } from 'vitest'
import { connectSSE } from './sse'
import { wails } from './wails'

type Handler = (data: unknown) => void

function fakeWails(connState = 'live') {
  const handlers = new Map<string, Handler>()
  const calls: string[] = []
  const runtime = {
    EventsOn(name: string, cb: Handler) {
      handlers.set(name, cb)
      return () => handlers.delete(name)
    },
    EventsOnce(name: string, cb: Handler) {
      handlers.set(name, (d) => {
        handlers.delete(name)
        cb(d)
      })
      return () => handlers.delete(name)
    },
    BrowserOpenURL(url: string) {
      calls.push('open ' + url)
    },
  }
  const started: string[] = []
  const app = {
    StartLogs: async (id: string, repo: string, name: string) => {
      // The listener must already exist when Go is asked to stream.
      if (!handlers.has('agentbox:log:' + id) || !handlers.has('agentbox:log:' + id + ':end')) {
        throw new Error('StartLogs called before subscribing to ' + id)
      }
      started.push(id)
      calls.push(`start ${repo} ${name}`)
    },
    StopLogs: async (id: string) => {
      calls.push('stop ' + id)
    },
    ConnState: async () => connState,
  }
  ;(globalThis as { window?: unknown }).window = { runtime, go: { main: { App: app } } }
  return { handlers, calls, started, fire: (name: string, data: unknown) => handlers.get(name)?.(data) }
}

afterEach(() => {
  delete (globalThis as { window?: unknown }).window
})

describe('wails()', () => {
  it('is null outside the app and non-null inside', () => {
    expect(wails()).toBeNull()
    fakeWails()
    expect(wails()).not.toBeNull()
  })
})

describe('connectSSE in the app', () => {
  function subscribe(w: ReturnType<typeof fakeWails>) {
    const got: string[] = []
    const states: string[] = []
    const r = { reconnects: 0 }
    const close = connectSSE({
      url: '/v1/events',
      events: ['box.state'],
      onEvent: (type, data) => got.push(type + ':' + JSON.stringify(data)),
      onState: (s) => states.push(s),
      onReconnect: () => r.reconnects++,
    })
    return { got, states, r, close, w }
  }

  it('maps the events stream and does not reload when the bridge is already live', async () => {
    const w = fakeWails('live')
    const { got, states, r, close } = subscribe(w)
    await vi.waitFor(() => expect(states).toEqual(['connecting', 'open']))
    expect(r.reconnects).toBe(0)
    w.fire('agentbox:event', { type: 'box.state', box: 'r/a', data: { state: 'running' } })
    expect(got).toEqual(['box.state:{"type":"box.state","box":"r/a","data":{"state":"running"}}'])
    w.fire('agentbox:conn', 'reconnecting')
    w.fire('agentbox:conn', 'live')
    expect(states).toEqual(['connecting', 'open', 'reconnecting', 'open'])
    expect(r.reconnects).toBe(1)
    close()
    expect(w.handlers.size).toBe(0)
  })

  it('reloads on the first live when the bridge was not live at subscribe time', async () => {
    const w = fakeWails('connecting')
    const { states, r, close } = subscribe(w)
    await vi.waitFor(() => expect(states).toEqual(['connecting', 'reconnecting']))
    w.fire('agentbox:conn', 'live')
    expect(r.reconnects).toBe(1)
    expect(states[states.length - 1]).toBe('open')
    close()
  })

  it('maps a logs url to StartLogs/StopLogs and per-line events', async () => {
    const w = fakeWails()
    const lines: string[] = []
    const close = connectSSE({
      url: '/v1/repos/app-1a2b3c/boxes/feature-x/logs?follow=true&tail=500',
      events: ['line'],
      onEvent: (_t, data) => lines.push(String(data)),
    })
    await vi.waitFor(() => expect(w.calls).toContain('start app-1a2b3c feature-x'))
    const id = w.started[0]
    expect(id.startsWith('app-1a2b3c/feature-x#')).toBe(true)
    w.fire('agentbox:log:' + id, 'listening on 20000')
    expect(lines).toEqual(['listening on 20000'])
    close()
    await vi.waitFor(() => expect(w.calls).toContain('stop ' + id))
    expect(w.handlers.size).toBe(0)
  })
})

describe('connectWails logs stream', () => {
  const URL = '/v1/repos/r/boxes/a/logs?follow=true&tail=500'
  function setup(starts: Array<Error | undefined>, connState = 'live') {
    const handlers = new Map<string, Handler>()
    const calls: string[] = []
    const ids: string[] = []
    let n = 0
    ;(globalThis as { window?: unknown }).window = {
      runtime: {
        EventsOn(name: string, cb: Handler) {
          handlers.set(name, cb)
          return () => handlers.delete(name)
        },
        EventsOnce(name: string, cb: Handler) {
          handlers.set(name, (d) => {
            handlers.delete(name)
            cb(d)
          })
          return () => handlers.delete(name)
        },
        BrowserOpenURL() {},
      },
      go: {
        main: {
          App: {
            StartLogs: async (id: string) => {
              if (!handlers.has('agentbox:log:' + id) || !handlers.has('agentbox:log:' + id + ':end')) {
                throw new Error('StartLogs called before subscribing to ' + id)
              }
              calls.push('start')
              ids.push(id)
              const r = starts[n++]
              if (r instanceof Error) throw r
            },
            StopLogs: async (id: string) => {
              calls.push('stop ' + id)
            },
            ConnState: async () => connState,
          },
        },
      },
    }
    const states: string[] = []
    const lines: unknown[] = []
    const r = { reconnects: 0 }
    const close = connectSSE({
      url: URL,
      events: ['line'],
      onEvent: (_t, d) => lines.push(d),
      onState: (s) => states.push(s),
      onReconnect: () => r.reconnects++,
    })
    return { handlers, calls, ids, states, lines, r, close }
  }

  afterEach(() => vi.useRealTimers())

  it('reconnects after :end', async () => {
    vi.useFakeTimers()
    const t = setup([undefined, undefined])
    await vi.advanceTimersByTimeAsync(0)
    const id1 = t.ids[0]
    expect(id1.startsWith('r/a#')).toBe(true)
    expect(t.handlers.has('agentbox:log:' + id1)).toBe(true)
    expect(t.handlers.has('agentbox:log:' + id1 + ':end')).toBe(true)
    expect(t.r.reconnects).toBe(0)
    t.handlers.get('agentbox:log:' + id1 + ':end')!(null)
    expect(t.states[t.states.length - 1]).toBe('reconnecting')
    expect(t.handlers.size).toBe(0)
    expect(t.calls.filter((c) => c === 'start')).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(t.calls.filter((c) => c === 'start')).toHaveLength(2)
    const id2 = t.ids[1]
    expect(id2).not.toBe(id1)
    expect(t.handlers.has('agentbox:log:' + id2)).toBe(true)
    expect(t.handlers.has('agentbox:log:' + id2 + ':end')).toBe(true)
    expect(t.states[t.states.length - 1]).toBe('open')
    expect(t.r.reconnects).toBe(1)
    t.handlers.get('agentbox:log:' + id2)!('hello')
    expect(t.lines).toEqual(['hello'])
    t.close()
    expect(t.calls).toContain('stop ' + id2)
    expect(t.calls).not.toContain('stop ' + id1)
    expect(t.handlers.size).toBe(0)
  })

  it('treats an :end that beats the StartLogs reply as a drop, not as open', async () => {
    vi.useFakeTimers()
    let release!: () => void
    const handlers = new Map<string, Handler>()
    const calls: string[] = []
    const ids: string[] = []
    ;(globalThis as { window?: unknown }).window = {
      runtime: {
        EventsOn(name: string, cb: Handler) {
          handlers.set(name, cb)
          return () => handlers.delete(name)
        },
        EventsOnce(name: string, cb: Handler) {
          handlers.set(name, (d) => {
            handlers.delete(name)
            cb(d)
          })
          return () => handlers.delete(name)
        },
        BrowserOpenURL() {},
      },
      go: {
        main: {
          App: {
            StartLogs: (id: string) => {
              ids.push(id)
              calls.push('start')
              return ids.length === 1 ? new Promise<void>((r) => (release = r)) : Promise.resolve()
            },
            StopLogs: async (id: string) => {
              calls.push('stop ' + id)
            },
            ConnState: async () => 'live',
          },
        },
      },
    }
    const states: string[] = []
    const close = connectSSE({ url: URL, events: ['line'], onEvent: () => {}, onState: (s) => states.push(s) })
    const id1 = ids[0]
    handlers.get('agentbox:log:' + id1 + ':end')!(null) // the daemon was shutting down
    expect(states[states.length - 1]).toBe('reconnecting')
    release() // now the original reply lands
    await vi.advanceTimersByTimeAsync(0)
    expect(states).not.toContain('open')
    expect(calls).not.toContain('stop ' + id1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(calls.filter((c) => c === 'start')).toHaveLength(2)
    expect(states[states.length - 1]).toBe('open')
    close()
  })

  it('retries after a rejected StartLogs', async () => {
    vi.useFakeTimers()
    const t = setup([new Error('not connected yet'), undefined])
    await vi.advanceTimersByTimeAsync(0)
    expect(t.states).toEqual(['connecting', 'reconnecting'])
    expect(t.calls.filter((c) => c === 'start')).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(t.calls.filter((c) => c === 'start')).toHaveLength(2)
    expect(t.states[t.states.length - 1]).toBe('open')
    expect(t.r.reconnects).toBe(0) // never opened before, so nothing to catch up on
    t.close()
  })

  it('does not let a stale ConnState snapshot override a live conn event', async () => {
    let resolve!: (s: string) => void
    const handlers = new Map<string, Handler>()
    ;(globalThis as { window?: unknown }).window = {
      runtime: {
        EventsOn(name: string, cb: Handler) {
          handlers.set(name, cb)
          return () => handlers.delete(name)
        },
        EventsOnce(name: string, cb: Handler) {
          handlers.set(name, cb)
          return () => handlers.delete(name)
        },
        BrowserOpenURL() {},
      },
      go: { main: { App: { StartLogs: async () => {}, StopLogs: async () => {}, ConnState: () => new Promise<string>((r) => (resolve = r)) } } },
    }
    const states: string[] = []
    const close = connectSSE({ url: '/v1/events', events: [], onEvent: () => {}, onState: (s) => states.push(s) })
    handlers.get('agentbox:conn')!('live')
    resolve('reconnecting')
    await Promise.resolve()
    await Promise.resolve()
    expect(states[states.length - 1]).toBe('open')
    close()
  })

  it('shows an unsupported stream url as not live instead of throwing', () => {
    setup([])
    const states: string[] = []
    const close = connectSSE({ url: '/v1/nope', events: [], onEvent: () => {}, onState: (s) => states.push(s) })
    expect(states[states.length - 1]).toBe('reconnecting')
    expect(() => close()).not.toThrow()
  })
})
