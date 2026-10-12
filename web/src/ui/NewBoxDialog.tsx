import { useEffect, useState, type FormEvent } from 'react'
import { api, describe } from '../api/client'
import type { RepoView } from '../api/types'
import { useBoxes } from '../store/boxes'
import { navigate } from '../store/route'
import { toasts } from '../store/toasts'
import { Modal } from './bits'

export function NewBoxDialog({ repo, onClose }: { repo: RepoView; onClose: () => void }) {
  const [name, setName] = useState('')
  const [from, setFrom] = useState('')
  const [noDev, setNoDev] = useState(false)
  const [id, setId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const state = useBoxes()
  const box = id ? state.byId[id] : undefined
  const step = id ? state.steps[id] : undefined
  const failure = id ? state.failures[id] : undefined

  useEffect(() => {
    if (box && (box.state === 'running' || box.state === 'ready')) {
      toasts.ok(`box ${box.name} is ${box.state}`)
      navigate({ kind: 'box', repo: box.repo, name: box.name })
      onClose()
    }
  }, [box, onClose])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    try {
      const c = await api.create({ repo_path: repo.path, name: name.trim(), from: from.trim() || undefined, no_dev: noDev || undefined })
      setId(c.id)
    } catch (err) {
      setError(describe(err))
    }
  }

  return (
    <Modal title="New box" onClose={onClose}>
      {!id ? (
        <form onSubmit={submit} className="form">
          <label>
            Name
            <input autoFocus value={name} onChange={(e) => setName(e.target.value)} pattern="[a-z0-9][a-z0-9-]{0,30}" required placeholder="feature-x" spellCheck={false} />
          </label>
          <label>
            Start from
            <input value={from} onChange={(e) => setFrom(e.target.value)} placeholder="HEAD (a branch, tag or commit)" spellCheck={false} />
          </label>
          <label className="check">
            <input type="checkbox" checked={noDev} onChange={(e) => setNoDev(e.target.checked)} /> Don't start the dev server
          </label>
          {error && <p className="error">{error}</p>}
          <div className="row end">
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button className="primary" type="submit">
              Create
            </button>
          </div>
        </form>
      ) : (
        <div className="stack">
          <p>
            Creating <span className="mono strong">{name}</span>
            {!failure && !box?.last_error && <span className="muted"> · {step ?? 'starting'}…</span>}
          </p>
          {failure && <p className="error">{failure}</p>}
          {!failure && box?.state === 'failed' && <p className="error">{box.last_error}</p>}
          {!failure && box?.state === 'stopped' && <p className="error">The dev server keeps crashing: {box.last_error}</p>}
          <div className="row end">
            <button onClick={onClose}>Close</button>
          </div>
        </div>
      )}
    </Modal>
  )
}
