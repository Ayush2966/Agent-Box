import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, describe } from '../api/client'
import type { RepoView } from '../api/types'
import { useBoxes } from '../store/boxes'
import { groupRepos, type RepoSummary } from '../store/repos'
import { navigate } from '../store/route'
import { toasts } from '../store/toasts'
import { Modal, StatePill } from './bits'

const terminal = new Set(['ready', 'running', 'stopped', 'failed'])

/**
 * Creates a box in `repo`, and optionally the same-named box in other repos at
 * the same time: one feature that spans several repos. Without a `repo` (the
 * cross-repo view) every repo is a checkbox and at least one must be ticked.
 */
export function NewBoxDialog({ repo, repos, onClose }: { repo?: RepoView; repos: RepoSummary[]; onClose: () => void }) {
  const [name, setName] = useState('')
  const [from, setFrom] = useState('')
  const [noDev, setNoDev] = useState(false)
  const [also, setAlso] = useState<Set<string>>(() => new Set())
  const [ids, setIds] = useState<{ repo: RepoSummary; id?: string; error?: string }[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const state = useBoxes()
  const others = useMemo(() => groupRepos(repos.filter((r) => r.id !== repo?.id)), [repos, repo?.id])
  const primary = repo ? repos.find((r) => r.id === repo.id) : undefined
  const count = also.size + (primary ? 1 : 0)

  const rows = ids?.map((t) => {
    const box = t.id ? state.byId[t.id] : undefined
    const step = t.id ? state.steps[t.id] : undefined
    const failure = (t.id && state.failures[t.id]) || t.error
    return { ...t, box, step, failure, done: !!failure || (!!box && terminal.has(box.state)) }
  })
  const allDone = !!rows && rows.every((r) => r.done)

  useEffect(() => {
    if (!rows || !allDone) return
    const ok = rows.filter((r) => r.box && (r.box.state === 'running' || r.box.state === 'ready'))
    if (rows.length === 1 && ok.length === 1) {
      const b = ok[0].box!
      toasts.ok(`box ${b.name} is ${b.state}`)
      navigate({ kind: 'box', repo: b.repo, name: b.name })
      onClose()
    } else if (rows.length > 1 && ok.length === rows.length) {
      toasts.ok(`${name} is up in ${rows.length} repos`)
      navigate({ kind: 'all', q: name })
      onClose()
    }
  }, [rows, allDone, name, onClose])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    const n = name.trim()
    const targets = [primary, ...repos.filter((r) => also.has(r.id))].filter((r): r is RepoSummary => !!r)
    if (targets.length === 0) {
      setError('Tick at least one repo.')
      return
    }
    const results = await Promise.all(
      targets.map(async (r) => {
        try {
          const c = await api.create({ repo_path: r.path, name: n, from: from.trim() || undefined, no_dev: noDev || undefined })
          return { repo: r, id: c.id }
        } catch (err) {
          return { repo: r, error: describe(err) }
        }
      }),
    )
    if (results.length === 1 && results[0].error) {
      setError(results[0].error)
      return
    }
    setIds(results)
  }

  const toggle = (id: string) =>
    setAlso((s) => {
      const next = new Set(s)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  return (
    <Modal title="New box" onClose={onClose}>
      {!rows ? (
        <form onSubmit={submit} className="form">
          <label>
            Name
            <input autoFocus value={name} onChange={(e) => setName(e.target.value)} pattern="[a-z0-9][a-z0-9-]{0,30}" required placeholder="feature-x" spellCheck={false} />
            <span className="hint">
              Lowercase letters, digits and dashes. The branch will be <code>agentbox/{name.trim() || 'feature-x'}</code>.
            </span>
          </label>
          <label>
            Start from
            <input value={from} onChange={(e) => setFrom(e.target.value)} placeholder="HEAD" spellCheck={false} />
            <span className="hint">A branch, tag or commit. A branch only on origin works by its plain name. Empty means the current HEAD.</span>
          </label>
          <label className="check">
            <input type="checkbox" checked={noDev} onChange={(e) => setNoDev(e.target.checked)} /> Don't start the dev server
          </label>
          {others.length > 0 && (
            <fieldset className="also">
              <legend>
                {primary ? (
                  <>
                    In <strong>{primary.name}</strong>
                    {also.size > 0 && <span className="muted"> and {also.size} more</span>}
                  </>
                ) : (
                  <>Create in {also.size > 0 && <span className="muted">{also.size} repos</span>}</>
                )}
              </legend>
              <p className="hint">
                {primary ? 'A feature that spans repos: tick the others and the same box is created in each.' : 'Tick every repo the feature touches; the same box is created in each.'}
              </p>
              <div className="also-groups">
                {others.map((g) => (
                  <div key={g.name || '*'} className="also-group">
                    {g.name && <div className="group-name">{g.name}</div>}
                    {g.repos.map((r) => (
                      <label key={r.id} className="check">
                        <input type="checkbox" checked={also.has(r.id)} onChange={() => toggle(r.id)} /> {r.name}
                      </label>
                    ))}
                  </div>
                ))}
              </div>
            </fieldset>
          )}
          {error && <p className="error">{error}</p>}
          <div className="row end">
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button className="primary" type="submit" disabled={count === 0}>
              {count > 1 ? `Create in ${count} repos` : 'Create'}
            </button>
          </div>
        </form>
      ) : (
        <div className="stack">
          <p>
            Creating <span className="mono strong">{name}</span>
            {rows.length > 1 ? ` in ${rows.length} repos` : ''}
          </p>
          <ul className="progress">
            {rows.map((r) => (
              <li key={r.repo.id}>
                <span className="repo-name strong">{r.repo.name}</span>
                {r.failure ? (
                  <span className="error">{r.failure}</span>
                ) : r.box && terminal.has(r.box.state) ? (
                  <StatePill state={r.box.state} />
                ) : (
                  <span className="pill pill-creating">{r.step ?? 'starting'}…</span>
                )}
              </li>
            ))}
          </ul>
          <p className="muted small">Worktree, ports, database copy, env file, setup, dev server. The box opens when it is ready.</p>
          <div className="row end">
            <button onClick={onClose}>Close</button>
          </div>
        </div>
      )}
    </Modal>
  )
}
