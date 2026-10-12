import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, describe } from '../api/client'

const MAX_BYTES = 1_000_000

export function DiffView({ repo, name, base }: { repo: string; name: string; base?: string }) {
  const [against, setAgainst] = useState(base ?? '')
  const [diff, setDiff] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(
    async (a: string) => {
      setLoading(true)
      setError(null)
      try {
        setDiff(await api.diff(repo, name, a))
      } catch (e) {
        setDiff(null)
        setError(describe(e))
      } finally {
        setLoading(false)
      }
    },
    [repo, name],
  )

  useEffect(() => {
    void load(base ?? '')
  }, [load, base])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    void load(against.trim())
  }

  const truncated = diff !== null && diff.length > MAX_BYTES
  const shown = truncated ? diff!.slice(0, MAX_BYTES) : diff

  return (
    <div className="diff">
      <form onSubmit={submit} className="row">
        <label className="inline">
          against
          <input value={against} onChange={(e) => setAgainst(e.target.value)} placeholder={base || 'main checkout branch'} spellCheck={false} />
        </label>
        <button disabled={loading}>Compare</button>
      </form>
      {error && <p className="error">{error}</p>}
      {shown === '' && <p className="muted">No changes.</p>}
      {truncated && <p className="muted">Diff truncated to 1 MB — use <code>agentbox exec {name} -- git diff</code> for the rest.</p>}
      {shown && (
        <pre className="diff-body">
          {shown.split('\n').map((l, i) => (
            <span key={i} className={lineClass(l)}>
              {l}
              {'\n'}
            </span>
          ))}
        </pre>
      )}
    </div>
  )
}

function lineClass(l: string): string {
  if (l.startsWith('+++') || l.startsWith('---')) return 'd-file'
  if (l.startsWith('diff --git')) return 'd-head'
  if (l.startsWith('@@')) return 'd-hunk'
  if (l.startsWith('+')) return 'd-add'
  if (l.startsWith('-')) return 'd-del'
  return ''
}
