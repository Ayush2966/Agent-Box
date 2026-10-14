import { useMemo, useState } from 'react'
import type { BoxView } from '../api/types'
import { navigate } from '../store/route'
import { BoxTable } from './BoxTable'
import { basename } from './bits'

/** Every box in every repo, in one table: the view for a feature that spans repos. */
export function AllBoxes({ boxes, steps, initialQuery, onNew }: { boxes: BoxView[]; steps: Record<string, string>; initialQuery?: string; onNew: () => void }) {
  const [q, setQ] = useState(initialQuery ?? '')
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase()
    const list = needle ? boxes.filter((b) => b.name.toLowerCase().includes(needle) || basename(b.repo_path).toLowerCase().includes(needle)) : boxes
    return [...list].sort((a, b) => a.name.localeCompare(b.name) || a.repo_path.localeCompare(b.repo_path))
  }, [boxes, q])
  const running = boxes.filter((b) => b.state === 'running').length
  return (
    <>
      <header className="repo-head">
        <div className="repo-title">
          <h1>All boxes</h1>
          <p className="muted small">
            {boxes.length} {boxes.length === 1 ? 'box' : 'boxes'} across every repo{running ? `, ${running} running` : ''}. Boxes that share a name are one
            feature across repos.
          </p>
        </div>
        <div className="row repo-actions">
          <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter by box or repo" aria-label="Filter boxes" spellCheck={false} />
          <button className="primary" onClick={onNew}>
            New box <kbd>n</kbd>
          </button>
        </div>
      </header>
      <BoxTable
        boxes={shown}
        steps={steps}
        emptyText={q ? `No box matches "${q}".` : 'No boxes yet.'}
        onOpen={(b) => navigate({ kind: 'box', repo: b.repo, name: b.name })}
      />
    </>
  )
}
