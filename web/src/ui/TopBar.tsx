import type { SSEState } from '../api/sse'
import { wails } from '../api/wails'
import type { Health } from '../api/types'

interface Props {
  health: Health | null
  sse: SSEState
  navOpen: boolean
  onToggleNav: () => void
}

export function TopBar(p: Props) {
  const conn = p.sse === 'open' ? 'live' : p.sse
  return (
    <header className="topbar">
      <button className="nav-toggle" aria-label="Repos" aria-expanded={p.navOpen} onClick={p.onToggleNav}>
        <span aria-hidden="true">☰</span> Repos
      </button>
      <a className="brand" href="#/">
        agentbox
      </a>
      <span className={`pill pill-conn-${p.sse}`} title={p.health ? `daemon pid ${p.health.pid} · ${p.health.version}` : 'connecting to the daemon'}>
        {conn}
      </span>
      <span className="spacer" />
      {!wails() && (
        <form method="post" action="/ui/logout">
          <button className="quiet">Sign out</button>
        </form>
      )}
    </header>
  )
}
