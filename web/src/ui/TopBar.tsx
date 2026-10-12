import type { SSEState } from '../api/sse'
import { wails } from '../api/wails'
import type { Health, RepoView } from '../api/types'
import { basename } from './bits'

interface Props {
  health: Health | null
  sse: SSEState
  repos: RepoView[]
  repo: string
  onRepo: (id: string) => void
  onNew: () => void
  onDoctor: () => void
  onRefreshDB: () => void
}

export function TopBar(p: Props) {
  const conn = p.sse === 'open' ? 'live' : p.sse
  return (
    <header className="topbar">
      <a className="brand" href="#/">
        agentbox
      </a>
      <span className={`pill pill-conn-${p.sse}`} title={p.health ? `daemon pid ${p.health.pid} · ${p.health.version}` : ''}>
        {conn}
      </span>
      <label className="repo-select">
        <span className="muted">repo</span>
        <select value={p.repo} onChange={(e) => p.onRepo(e.target.value)} disabled={p.repos.length === 0}>
          {p.repos.length === 0 && <option value="">no repos yet</option>}
          {p.repos.map((r) => (
            <option key={r.id} value={r.id} title={r.path}>
              {basename(r.path)}
            </option>
          ))}
        </select>
      </label>
      <span className="spacer" />
      <button onClick={p.onDoctor} disabled={!p.repo}>
        Doctor
      </button>
      <button onClick={p.onRefreshDB} disabled={!p.repo}>
        Refresh DB
      </button>
      <button className="primary" onClick={p.onNew} disabled={!p.repo}>
        New box <kbd>n</kbd>
      </button>
      {!wails() && (
        <form method="post" action="/ui/logout">
          <button className="quiet">Sign out</button>
        </form>
      )}
    </header>
  )
}
