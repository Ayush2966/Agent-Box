import { describe, expect, it } from 'vitest'
import type { BoxView } from '../api/types'
import { boxesReducer, initialBoxes, selectBoxes, splitId } from './boxes'

const box = (over: Partial<BoxView>): BoxView => ({
  id: 'r/a', repo: 'r', repo_path: '/r', name: 'a', dir: '/d', branch: 'agentbox/a', state: 'ready',
  ports: {}, ahead: 0, behind: 0, dirty: 0, created_at: '2026-10-09T00:00:00Z', ...over,
})

describe('boxesReducer', () => {
  it('loads a list and selects per repo, sorted by name', () => {
    const s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({ id: 'r/b', name: 'b' }), box({}), box({ id: 'q/z', repo: 'q', name: 'z' })] })
    expect(selectBoxes(s, 'r').map((b) => b.name)).toEqual(['a', 'b'])
    expect(selectBoxes(s, '').map((b) => b.name)).toEqual(['a', 'b', 'z'])
  })

  it('applies box.state to a known box and clears its step on a terminal state', () => {
    let s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({ state: 'creating' })] })
    s = boxesReducer(s, { type: 'event', event: { type: 'box.step', box: 'r/a', data: { step: 'setup' } } })
    expect(s.steps['r/a']).toBe('setup')
    s = boxesReducer(s, { type: 'event', event: { type: 'box.state', box: 'r/a', data: { state: 'running' } } })
    expect(s.byId['r/a'].state).toBe('running')
    expect(s.steps['r/a']).toBeUndefined()
  })

  it('keeps steps and failures for boxes it has not loaded yet', () => {
    let s = boxesReducer(initialBoxes, { type: 'event', event: { type: 'box.step', box: 'r/n', data: { step: 'worktree' } } })
    expect(s.steps['r/n']).toBe('worktree')
    s = boxesReducer(s, { type: 'event', event: { type: 'box.state', box: 'r/n', data: { state: 'failed', error: 'setup failed: boom' } } })
    expect(s.byId['r/n']).toBeUndefined()
    expect(s.steps['r/n']).toBeUndefined() // failed is terminal: the step is done
    expect(s.failures['r/n']).toBe('setup failed: boom')
  })

  it('removes a box on box.removed but remembers why', () => {
    let s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({})] })
    s = boxesReducer(s, { type: 'event', event: { type: 'box.removed', box: 'r/a', data: { error: 'rolled back' } } })
    expect(s.byId['r/a']).toBeUndefined()
    expect(s.failures['r/a']).toBe('rolled back')
    const again = boxesReducer(s, { type: 'load', boxes: [] })
    expect(again.failures).toEqual({})
  })

  it('upsert replaces a box and returns the same state for unknown event types', () => {
    let s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({})] })
    s = boxesReducer(s, { type: 'upsert', box: box({ state: 'stopped', dirty: 3 }) })
    expect(s.byId['r/a']).toMatchObject({ state: 'stopped', dirty: 3 })
    expect(boxesReducer(s, { type: 'event', event: { type: 'nope' as never, box: 'r/a' } })).toBe(s)
  })
})

describe('splitId', () => {
  it('splits on the first slash only', () => {
    expect(splitId('myapp-1a2b3c/feature-x')).toEqual({ repo: 'myapp-1a2b3c', name: 'feature-x' })
    expect(splitId('solo')).toEqual({ repo: '', name: 'solo' })
  })
})
