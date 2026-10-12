import { ApiError, SignedOut } from '../api/client'
import { createStore } from './store'

export interface Toast {
  id: number
  kind: 'error' | 'ok'
  title: string
  message?: string
  hint?: string
}

type Action = { type: 'add'; toast: Toast } | { type: 'dismiss'; id: number }

function reducer(s: Toast[], a: Action): Toast[] {
  switch (a.type) {
    case 'add':
      return [...s.slice(-4), a.toast]
    case 'dismiss':
      return s.filter((t) => t.id !== a.id)
  }
}

const store = createStore(reducer, [] as Toast[])
let seq = 0

function add(t: Omit<Toast, 'id'>, ttlMs: number) {
  const id = ++seq
  store.dispatch({ type: 'add', toast: { ...t, id } })
  setTimeout(() => store.dispatch({ type: 'dismiss', id }), ttlMs)
}

export const toasts = {
  use: store.use,
  dismiss: (id: number) => store.dispatch({ type: 'dismiss', id }),
  ok: (title: string, message?: string) => add({ kind: 'ok', title, message }, 3_000),
  /** Shows any thrown value; a 401 is handled by the sign-in screen and not shown. */
  error: (e: unknown) => {
    if (e instanceof SignedOut) return
    if (e instanceof ApiError) add({ kind: 'error', title: e.title, message: e.message, hint: e.hint }, 8_000)
    else add({ kind: 'error', title: 'error', message: e instanceof Error ? e.message : String(e) }, 8_000)
  },
}
