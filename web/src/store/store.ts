import { useSyncExternalStore } from 'react'

type Listener = () => void

/** A tiny reducer store with a React hook. */
export function createStore<S, A>(reducer: (s: S, a: A) => S, initial: S) {
  let state = initial
  const listeners = new Set<Listener>()
  const get = () => state
  const subscribe = (l: Listener) => {
    listeners.add(l)
    return () => {
      listeners.delete(l)
    }
  }
  const dispatch = (a: A) => {
    const next = reducer(state, a)
    if (next !== state) {
      state = next
      listeners.forEach((l) => l())
    }
  }
  const use = () => useSyncExternalStore(subscribe, get, get)
  return { get, subscribe, dispatch, use }
}
