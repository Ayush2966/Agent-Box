import type { RepoSummary } from '../store/repos'
import { describeCounts } from '../store/repos'
import { CopyButton } from './bits'

interface Props {
  repo: RepoSummary
  onNew: () => void
  onDoctor: () => void
  onRefreshDB: () => void
  onPrune: () => void
}

export function RepoHeader({ repo, onNew, onDoctor, onRefreshDB, onPrune }: Props) {
  return (
    <header className="repo-head">
      <div className="repo-title">
        <h1>{repo.name}</h1>
        <div className="repo-path">
          <code className="muted">{repo.path}</code>
          <CopyButton text={repo.path} label="Copy" className="quiet small" title="Copy the repo path" />
        </div>
        <p className="muted small">{describeCounts(repo)}</p>
      </div>
      <div className="row repo-actions">
        <button onClick={onDoctor} title="Find and safely repair drift between agentbox and reality">
          Doctor
        </button>
        <button onClick={onRefreshDB} title="Re-snapshot the source database for new boxes">
          Refresh DB
        </button>
        <button onClick={onPrune} title="Remove this repo's stopped and failed boxes">
          Prune
        </button>
        <button className="primary" onClick={onNew}>
          New box <kbd>n</kbd>
        </button>
      </div>
    </header>
  )
}
