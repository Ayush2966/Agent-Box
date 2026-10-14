import type { BoxView, RepoView } from '../api/types'
import { formatRoute } from '../store/route'
import { RunButtons } from './RunButtons'
import { CopyButton, ExternalLink, StatePill, basename, formatPorts } from './bits'

interface Props {
  boxes: BoxView[]
  steps: Record<string, string>
  /** The repo being shown; absent in the cross-repo view, which adds a Repo column. */
  repo?: RepoView
  emptyText?: string
  onOpen: (b: BoxView) => void
}

/** The host of a box URL: what tells boxes apart, without the scheme and the proxy port. */
function hostOf(url: string): string {
  try {
    return new URL(url).hostname
  } catch {
    return url
  }
}

function Num({ n, sign, title }: { n: number; sign: string; title: string }) {
  return (
    <span className={n ? 'git-n' : 'git-n zero'} title={`${n} ${title}`}>
      {sign}
      {n}
    </span>
  )
}

export function BoxTable({ boxes, steps, repo, emptyText, onOpen }: Props) {
  if (boxes.length === 0) {
    return (
      <div className="empty card">
        <p className="strong">{emptyText ?? (repo ? 'No boxes in this repo yet.' : 'No boxes yet.')}</p>
        {repo && (
          <>
            <p className="muted">Press <kbd>n</kbd> or run:</p>
            <pre className="copy">
              cd {JSON.stringify(repo.path)}
              {'\n'}agentbox new feature-x
            </pre>
          </>
        )}
      </div>
    )
  }
  return (
    <div className="table-wrap">
      <table className="boxes">
        <thead>
          <tr>
            <th>Box</th>
            {!repo && <th>Repo</th>}
            <th>Status</th>
            <th>URL</th>
            <th className="secondary">Ports</th>
            <th className="git" title="commits ahead ↑ / behind ↓ the base branch · uncommitted files ●">
              Git
            </th>
            <th aria-label="Actions"></th>
          </tr>
        </thead>
        <tbody>
          {boxes.map((b) => (
            <tr key={b.id} className={`row-${b.state}`}>
              <td>
                <a className="box-name mono strong" href={formatRoute({ kind: 'box', repo: b.repo, name: b.name })}>
                  {b.name}
                </a>
                <div className="box-branch mono muted">{b.branch}</div>
              </td>
              {!repo && (
                <td>
                  <a href={formatRoute({ kind: 'repo', repo: b.repo })} title={b.repo_path} className="repo-link">
                    {basename(b.repo_path)}
                  </a>
                </td>
              )}
              <td>
                <StatePill state={b.state} />
                {steps[b.id] && <div className="muted step">{steps[b.id]}…</div>}
                {b.state === 'failed' && b.last_error && (
                  <div className="error step" title={b.last_error}>
                    {b.last_error}
                  </div>
                )}
              </td>
              <td>
                {b.url ? (
                  <ExternalLink className="mono" href={b.url} title={`${b.url} (opens in your browser)`}>
                    {hostOf(b.url)} ↗
                  </ExternalLink>
                ) : (
                  <span className="muted">—</span>
                )}
              </td>
              <td className="mono secondary">{formatPorts(b.ports)}</td>
              <td className="git mono">
                <Num n={b.ahead} sign="↑" title="commits ahead" /> <Num n={b.behind} sign="↓" title="commits behind" />{' '}
                <Num n={b.dirty} sign="●" title="uncommitted files" />
              </td>
              <td className="actions">
                <RunButtons box={b} />
                <CopyButton text={b.dir} label="Copy path" title={`Copy ${b.dir}`} />
                <button onClick={() => onOpen(b)}>Open</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
