import type { BoxView, RepoView } from '../api/types'

/** One repo as the sidebar shows it: name, where it lives, and what its boxes are doing. */
export interface RepoSummary {
  id: string
  path: string
  name: string
  /** The parent folder's name — repos are grouped by it. */
  group: string
  total: number
  running: number
  failed: number
  creating: number
}

export interface RepoGroup {
  name: string
  repos: RepoSummary[]
}

function segments(path: string): string[] {
  return path.split('/').filter(Boolean)
}

export function summarizeRepos(repos: RepoView[], boxes: BoxView[]): RepoSummary[] {
  const counts: Record<string, Pick<RepoSummary, 'total' | 'running' | 'failed' | 'creating'>> = {}
  for (const b of boxes) {
    const c = (counts[b.repo] ??= { total: 0, running: 0, failed: 0, creating: 0 })
    c.total++
    if (b.state === 'running') c.running++
    if (b.state === 'failed') c.failed++
    if (b.state === 'creating') c.creating++
  }
  return repos
    .map((r) => {
      const s = segments(r.path)
      return {
        id: r.id,
        path: r.path,
        name: s[s.length - 1] ?? r.path,
        group: s.length > 1 ? s[s.length - 2] : '',
        ...(counts[r.id] ?? { total: 0, running: 0, failed: 0, creating: 0 }),
      }
    })
    .sort((a, b) => a.name.localeCompare(b.name) || a.path.localeCompare(b.path))
}

/** Groups by parent folder (alphabetical). A single group keeps no header name. */
export function groupRepos(repos: RepoSummary[]): RepoGroup[] {
  const byGroup = new Map<string, RepoSummary[]>()
  for (const r of repos) {
    const list = byGroup.get(r.group) ?? []
    list.push(r)
    byGroup.set(r.group, list)
  }
  const groups = [...byGroup.entries()].map(([name, repos]) => ({ name, repos })).sort((a, b) => a.name.localeCompare(b.name))
  if (groups.length === 1) groups[0].name = ''
  return groups
}

/** Case-insensitive match on the repo name or any part of its path. */
export function filterRepos(repos: RepoSummary[], query: string): RepoSummary[] {
  const q = query.trim().toLowerCase()
  if (!q) return repos
  return repos.filter((r) => r.name.toLowerCase().includes(q) || r.path.toLowerCase().includes(q))
}

export function describeCounts(r: RepoSummary): string {
  if (r.total === 0) return 'no boxes'
  const parts = [`${r.total} ${r.total === 1 ? 'box' : 'boxes'}`]
  if (r.running) parts.push(`${r.running} running`)
  if (r.creating) parts.push(`${r.creating} creating`)
  if (r.failed) parts.push(`${r.failed} failed`)
  return parts.join(', ')
}
