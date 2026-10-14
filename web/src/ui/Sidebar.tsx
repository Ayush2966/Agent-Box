import type { Health } from '../api/types'
import { describeCounts, type RepoGroup } from '../store/repos'
import { formatRoute } from '../store/route'

interface Props {
  groups: RepoGroup[]
  total: number
  boxes: number
  /** The active repo id, or 'all' for the cross-repo view. */
  active: string
  query: string
  onQuery: (q: string) => void
  open: boolean
  onClose: () => void
  health: Health | null
}

const FILTER_FROM = 7

export function Sidebar(p: Props) {
  const shown = p.groups.reduce((n, g) => n + g.repos.length, 0)
  return (
    <>
      {p.open && <div className="nav-backdrop" onMouseDown={p.onClose} />}
      <nav className={p.open ? 'sidebar open' : 'sidebar'} aria-label="Repos">
        <a className={p.active === 'all' ? 'repo-item all active' : 'repo-item all'} href={formatRoute({ kind: 'all' })} onClick={p.onClose} aria-current={p.active === 'all' ? 'page' : undefined}>
          <span className="repo-name">All boxes</span>
          <span className="repo-counts">{p.boxes > 0 && <span className="count">{p.boxes}</span>}</span>
        </a>
        <div className="side-head">
          <span className="strong">Repos</span>
          <span className="muted">{p.total}</span>
        </div>
        {p.total >= FILTER_FROM && (
          <input
            className="side-filter"
            type="search"
            value={p.query}
            onChange={(e) => p.onQuery(e.target.value)}
            placeholder="Filter repos"
            aria-label="Filter repos"
            spellCheck={false}
          />
        )}
        {p.total === 0 && <p className="side-empty muted">Run `agentbox init` in a repo and it appears here.</p>}
        {p.total > 0 && shown === 0 && <p className="side-empty muted">No repo matches.</p>}
        {p.groups.map((g) => (
          <section key={g.name || '*'} className="repo-group">
            {g.name && <h3 className="group-name">{g.name}</h3>}
            <ul className="repo-list">
              {g.repos.map((r) => (
                <li key={r.id}>
                  <a
                    className={r.id === p.active ? 'repo-item active' : 'repo-item'}
                    href={formatRoute({ kind: 'repo', repo: r.id })}
                    title={`${r.path}\n${describeCounts(r)}`}
                    aria-current={r.id === p.active ? 'page' : undefined}
                    onClick={p.onClose}
                  >
                    <span className="repo-name">{r.name}</span>
                    <span className="repo-counts" aria-label={describeCounts(r)}>
                      {r.creating > 0 && <span className="dot dot-creating" />}
                      {r.failed > 0 && <span className="dot dot-failed" />}
                      {r.running > 0 && <span className="dot dot-running" />}
                      {r.total > 0 && <span className="count">{r.total}</span>}
                    </span>
                  </a>
                </li>
              ))}
            </ul>
          </section>
        ))}
        <div className="side-foot muted">{p.health ? `daemon ${p.health.version} · pid ${p.health.pid}` : 'daemon: connecting…'}</div>
      </nav>
    </>
  )
}
