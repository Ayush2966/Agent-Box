import { useMemo, useSyncExternalStore } from 'react'

export type Route = { kind: 'home' } | { kind: 'all'; q?: string } | { kind: 'repo'; repo: string } | { kind: 'box'; repo: string; name: string }

function decode(segment: string): string {
  try {
    return decodeURIComponent(segment)
  } catch {
    return segment
  }
}

export function parseRoute(hash: string): Route {
  const parts = hash
    .replace(/^#\/?/, '')
    .split('/')
    .filter(Boolean)
    .map(decode)
  if (parts[0] === 'all') return parts[1] ? { kind: 'all', q: parts[1] } : { kind: 'all' }
  if (parts[0] === 'r' && parts[1]) {
    if (parts[2] === 'b' && parts[3]) return { kind: 'box', repo: parts[1], name: parts[3] }
    return { kind: 'repo', repo: parts[1] }
  }
  return { kind: 'home' }
}

export function formatRoute(r: Route): string {
  switch (r.kind) {
    case 'home':
      return '#/'
    case 'all':
      return r.q ? `#/all/${encodeURIComponent(r.q)}` : '#/all'
    case 'repo':
      return `#/r/${encodeURIComponent(r.repo)}`
    case 'box':
      return `#/r/${encodeURIComponent(r.repo)}/b/${encodeURIComponent(r.name)}`
  }
}

export function navigate(r: Route) {
  location.hash = formatRoute(r)
}

const subscribe = (l: () => void) => {
  addEventListener('hashchange', l)
  return () => removeEventListener('hashchange', l)
}
const getHash = () => location.hash
const getServerHash = () => ''

export function useRoute(): Route {
  const hash = useSyncExternalStore(subscribe, getHash, getServerHash)
  return useMemo(() => parseRoute(hash), [hash])
}
