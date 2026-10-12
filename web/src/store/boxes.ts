import type { BoxEvent, BoxState, BoxView } from '../api/types'
import { createStore } from './store'

export interface BoxesState {
  byId: Record<string, BoxView>
  steps: Record<string, string> // box id → current `new` step, while creating
  failures: Record<string, string> // box id → error text from a failed/rolled-back creation
}

export type BoxesAction =
  | { type: 'load'; boxes: BoxView[] }
  | { type: 'upsert'; box: BoxView }
  | { type: 'event'; event: BoxEvent }

export const initialBoxes: BoxesState = { byId: {}, steps: {}, failures: {} }

const terminal = new Set<string>(['ready', 'running', 'stopped', 'failed'])

function without<T>(m: Record<string, T>, key: string): Record<string, T> {
  if (!(key in m)) return m
  const copy = { ...m }
  delete copy[key]
  return copy
}

export function boxesReducer(s: BoxesState, a: BoxesAction): BoxesState {
  switch (a.type) {
    case 'load': {
      const byId: Record<string, BoxView> = {}
      for (const b of a.boxes) byId[b.id] = b
      return { byId, steps: s.steps, failures: {} }
    }
    case 'upsert':
      return { ...s, byId: { ...s.byId, [a.box.id]: a.box } }
    case 'event': {
      const { type, box: id, data } = a.event
      const error = typeof data?.error === 'string' ? data.error : undefined
      switch (type) {
        case 'box.step':
          return { ...s, steps: { ...s.steps, [id]: String(data?.step ?? '') } }
        case 'box.state': {
          const state = String(data?.state ?? '') as BoxState
          const steps = terminal.has(state) ? without(s.steps, id) : s.steps
          const failures = error ? { ...s.failures, [id]: error } : s.failures
          const cur = s.byId[id]
          if (!cur) return { ...s, steps, failures }
          const last_error = typeof data?.last_error === 'string' ? data.last_error : error ?? cur.last_error
          return { byId: { ...s.byId, [id]: { ...cur, state, last_error } }, steps, failures }
        }
        case 'box.removed':
          return { byId: without(s.byId, id), steps: without(s.steps, id), failures: error ? { ...s.failures, [id]: error } : s.failures }
        case 'box.created':
          return s
        default:
          return s
      }
    }
  }
}

export const boxes = createStore(boxesReducer, initialBoxes)
export const useBoxes = boxes.use

export function splitId(id: string): { repo: string; name: string } {
  const i = id.indexOf('/')
  return i < 0 ? { repo: '', name: id } : { repo: id.slice(0, i), name: id.slice(i + 1) }
}

export function selectBoxes(s: BoxesState, repo: string): BoxView[] {
  return Object.values(s.byId)
    .filter((b) => !repo || b.repo === repo)
    .sort((a, b) => a.name.localeCompare(b.name))
}
