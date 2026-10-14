import { describe, expect, it } from 'vitest'
import type { BoxView } from '../api/types'
import { describeCounts, filterRepos, groupRepos, summarizeRepos } from './repos'

const box = (repo: string, name: string, state: BoxView['state']): BoxView =>
  ({ id: `${repo}/${name}`, repo, name, state, ports: {}, ahead: 0, behind: 0, dirty: 0 }) as unknown as BoxView

const repos = [
  { id: 'rms-1', path: '/Users/me/Desktop/Frontend Apps/pl-web-rms' },
  { id: 'admin-2', path: '/Users/me/Desktop/Backend services/pl-api-admin-service' },
  { id: 'panel-3', path: '/Users/me/Desktop/Frontend Apps/pl-web-control-panel' },
]

describe('summarizeRepos', () => {
  it('names repos by their folder, groups them by the parent, and counts their boxes', () => {
    const s = summarizeRepos(repos, [box('rms-1', 'a', 'running'), box('rms-1', 'b', 'failed'), box('rms-1', 'c', 'stopped'), box('admin-2', 'x', 'creating')])
    expect(s.map((r) => r.name)).toEqual(['pl-api-admin-service', 'pl-web-control-panel', 'pl-web-rms'])
    const rms = s.find((r) => r.id === 'rms-1')!
    expect(rms).toMatchObject({ group: 'Frontend Apps', total: 3, running: 1, failed: 1, creating: 0 })
    expect(s.find((r) => r.id === 'admin-2')).toMatchObject({ group: 'Backend services', total: 1, creating: 1 })
    expect(s.find((r) => r.id === 'panel-3')).toMatchObject({ total: 0, running: 0 })
  })
})

describe('groupRepos', () => {
  it('orders groups by name and drops the header when there is only one', () => {
    const g = groupRepos(summarizeRepos(repos, []))
    expect(g.map((x) => x.name)).toEqual(['Backend services', 'Frontend Apps'])
    expect(g[1].repos.map((r) => r.name)).toEqual(['pl-web-control-panel', 'pl-web-rms'])
    expect(groupRepos(summarizeRepos(repos.slice(0, 1), []))[0].name).toBe('')
  })
})

describe('filterRepos', () => {
  it('matches the name or any part of the path, ignoring case', () => {
    const s = summarizeRepos(repos, [])
    expect(filterRepos(s, 'RMS').map((r) => r.id)).toEqual(['rms-1'])
    expect(filterRepos(s, 'backend').map((r) => r.id)).toEqual(['admin-2'])
    expect(filterRepos(s, '  ')).toHaveLength(3)
  })
})

describe('describeCounts', () => {
  it('reads naturally', () => {
    const s = summarizeRepos(repos, [box('rms-1', 'a', 'running'), box('rms-1', 'b', 'failed')])
    expect(describeCounts(s.find((r) => r.id === 'rms-1')!)).toBe('2 boxes, 1 running, 1 failed')
    expect(describeCounts(s.find((r) => r.id === 'panel-3')!)).toBe('no boxes')
  })
})
