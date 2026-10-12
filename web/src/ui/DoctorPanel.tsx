import { useCallback, useEffect, useState } from 'react'
import { api, describe } from '../api/client'
import type { Finding, RepoView } from '../api/types'
import { Modal } from './bits'

export function DoctorPanel({ repo, onClose }: { repo: RepoView; onClose: () => void }) {
  const [findings, setFindings] = useState<Finding[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const run = useCallback(
    async (fix: boolean) => {
      setBusy(true)
      setError(null)
      try {
        setFindings(await api.doctor(repo.id, fix))
      } catch (e) {
        setError(describe(e))
      } finally {
        setBusy(false)
      }
    },
    [repo.id],
  )

  useEffect(() => {
    void run(false)
  }, [run])

  const unfixed = findings?.filter((f) => !f.fixed).length ?? 0

  return (
    <Modal title="Doctor" onClose={onClose} wide>
      <p className="muted">{repo.path}</p>
      {error && <p className="error">{error}</p>}
      {findings === null && !error && <p className="muted">Checking…</p>}
      {findings?.length === 0 && <p className="ok">✓ No problems found.</p>}
      {findings && findings.length > 0 && (
        <ul className="findings">
          {findings.map((f, i) => (
            <li key={i} className={f.fixed ? 'fixed' : ''}>
              <span className="pill pill-kind">{f.kind}</span> {f.fixed ? '✓ fixed · ' : ''}
              {f.detail}
            </li>
          ))}
        </ul>
      )}
      <div className="row end">
        <button onClick={() => run(false)} disabled={busy}>
          Re-check
        </button>
        <button className="primary" onClick={() => run(true)} disabled={busy || unfixed === 0}>
          Fix what is safe
        </button>
      </div>
    </Modal>
  )
}
