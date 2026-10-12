import type { BoxView, RepoView } from '../api/types'
import { formatRoute } from '../store/route'
import { RunButtons } from './RunButtons'
import { StatePill, formatPorts } from './bits'

interface Props {
  boxes: BoxView[]
  steps: Record<string, string>
  repo: RepoView
  onOpen: (b: BoxView) => void
}

export function BoxTable({ boxes, steps, repo, onOpen }: Props) {
  if (boxes.length === 0) {
    return (
      <div className="empty">
        <p>No boxes in this repo yet.</p>
        <pre className="copy">
          cd {repo.path}
          {'\n'}agentbox new feature-x
        </pre>
      </div>
    )
  }
  return (
    <div className="table-wrap">
      <p className="muted path">{repo.path}</p>
      <table className="boxes">
        <thead>
          <tr>
            <th>Name</th>
            <th>State</th>
            <th>URL</th>
            <th className="secondary">Ports</th>
            <th className="num">Ahead</th>
            <th className="num secondary">Behind</th>
            <th className="num">Dirty</th>
            <th aria-label="Actions"></th>
          </tr>
        </thead>
        <tbody>
          {boxes.map((b) => (
            <tr key={b.id}>
              <td>
                <a className="mono strong" href={formatRoute({ kind: 'box', repo: b.repo, name: b.name })}>
                  {b.name}
                </a>
              </td>
              <td>
                <StatePill state={b.state} />
                {steps[b.id] && <span className="muted step"> {steps[b.id]}…</span>}
                {b.state === 'failed' && b.last_error && <span className="error step"> {b.last_error}</span>}
              </td>
              <td>
                {b.url ? (
                  <a className="mono" href={b.url} target="_blank" rel="noreferrer">
                    {b.url.replace(/^http:\/\//, '').replace(/\/$/, '')}
                  </a>
                ) : (
                  <span className="muted">—</span>
                )}
              </td>
              <td className="mono secondary">{formatPorts(b.ports)}</td>
              <td className="num">{b.ahead}</td>
              <td className="num secondary">{b.behind}</td>
              <td className="num">{b.dirty}</td>
              <td className="actions">
                <RunButtons box={b} />
                <button onClick={() => onOpen(b)}>Open</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
