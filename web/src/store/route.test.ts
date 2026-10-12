import { describe, expect, it } from 'vitest'
import { formatRoute, parseRoute, type Route } from './route'

describe('routes', () => {
  it('round-trips every route kind', () => {
    const routes: Route[] = [
      { kind: 'home' },
      { kind: 'repo', repo: 'myapp-1a2b3c' },
      { kind: 'box', repo: 'myapp-1a2b3c', name: 'feature-x' },
    ]
    for (const r of routes) expect(parseRoute(formatRoute(r))).toEqual(r)
  })

  it('tolerates missing hashes and junk', () => {
    expect(parseRoute('')).toEqual({ kind: 'home' })
    expect(parseRoute('#')).toEqual({ kind: 'home' })
    expect(parseRoute('#/r')).toEqual({ kind: 'home' })
    expect(parseRoute('#/r/x/b')).toEqual({ kind: 'repo', repo: 'x' })
    expect(parseRoute('#/what/ever')).toEqual({ kind: 'home' })
  })

  it('decodes encoded segments', () => {
    expect(parseRoute('#/r/a%2Fb/b/c')).toEqual({ kind: 'box', repo: 'a/b', name: 'c' })
    expect(formatRoute({ kind: 'repo', repo: 'a/b' })).toBe('#/r/a%2Fb')
  })

  it('never throws on a malformed hash', () => {
    expect(parseRoute('#/r/100%')).toEqual({ kind: 'repo', repo: '100%' })
    expect(parseRoute('#/r/%E0%A4%A/b/x')).toEqual({ kind: 'box', repo: '%E0%A4%A', name: 'x' })
  })
})
