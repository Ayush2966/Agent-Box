import { Fragment, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { ApiError, api } from '../api/client'
import type { BoxView } from '../api/types'
import { boxes, useBoxes } from '../store/boxes'
import { toasts } from '../store/toasts'
import { DiffView } from './DiffView'
import { LogView } from './LogView'
import { RunButtons } from './RunButtons'
import { StatePill, formatPorts } from './bits'
import { ConfirmDialog } from './confirm'

type Tab = 'overview' | 'logs' | 'diff' | 'danger'
const TABS: Tab[] = ['overview', 'logs', 'diff', 'danger']

export function BoxPage({ repo, name, onBack }: { repo: string; name: string; onBack: () => void }) {
  const state = useBoxes()
  const id = `${repo}/${name}`
  const box = state.byId[id]
  const [tab, setTab] = useState<Tab>('overview')
  const [missing, setMissing] = useState(false)

  useEffect(() => {
    if (box) return
    api
      .box(repo, name)
      .then((b) => boxes.dispatch({ type: 'upsert', box: b }))
      .catch(() => setMissing(true))
  }, [box, repo, name])

  if (!box) {
    return (
      <section className="page">
        <p className="muted">{missing ? `No box called ${name} in this repo.` : 'Loading…'}</p>
        <button onClick={onBack}>Back to boxes</button>
      </section>
    )
  }

  return (
    <section className="page">
      <header className="page-head">
        <button className="quiet" onClick={onBack}>
          ← boxes
        </button>
        <h1 className="mono">{box.name}</h1>
        <StatePill state={box.state} />
        {state.steps[id] && <span className="muted">{state.steps[id]}…</span>}
        <span className="spacer" />
        {box.url && (
          <a className="button" href={box.url} target="_blank" rel="noreferrer">
            Open app ↗
          </a>
        )}
        <RunButtons box={box} />
      </header>
      <nav className="tabs" role="tablist">
        {TABS.map((t) => (
          <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? 'tab active' : 'tab'} onClick={() => setTab(t)}>
            {t}
          </button>
        ))}
      </nav>
      {tab === 'overview' && <Overview box={box} />}
      {tab === 'logs' && <LogView repo={repo} name={name} />}
      {tab === 'diff' && <DiffView repo={repo} name={name} base={box.base} />}
      {tab === 'danger' && <Danger box={box} onRemoved={onBack} />}
    </section>
  )
}

function Overview({ box }: { box: BoxView }) {
  const rows: [string, ReactNode][] = [
    ['dir', <code>{box.dir}</code>],
    ['branch', <code>{box.branch}</code>],
    ['base', box.base ?? '—'],
    ['ahead / behind', `${box.ahead} / ${box.behind}`],
    ['dirty files', box.dirty],
    ['ports', <code>{formatPorts(box.ports)}</code>],
    [
      'url',
      box.url ? (
        <a href={box.url} target="_blank" rel="noreferrer">
          {box.url}
        </a>
      ) : (
        '—'
      ),
    ],
    ['database', box.db_name ?? '—'],
    ['pid', box.pid ?? '—'],
    ['created', new Date(box.created_at).toLocaleString()],
  ]
  if (box.last_error) rows.push(['last error', <span className="error">{box.last_error}</span>])
  const env = Object.entries(box.env ?? {}).sort(([a], [b]) => a.localeCompare(b))
  return (
    <div className="stack">
      <dl className="facts">
        {rows.map(([k, v]) => (
          <Fragment key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </Fragment>
        ))}
      </dl>
      <h3>Point your agent at it</h3>
      <pre className="copy">cd {JSON.stringify(box.dir)}</pre>
      {env.length > 0 && (
        <>
          <h3>Managed env</h3>
          <table className="env">
            <tbody>
              {env.map(([k, v]) => (
                <tr key={k}>
                  <th className="mono">{k}</th>
                  <td>
                    <code>{v}</code>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}

function Danger({ box, onRemoved }: { box: BoxView; onRemoved: () => void }) {
  const [into, setInto] = useState('')
  const [squash, setSquash] = useState(false)
  const [keepBranch, setKeepBranch] = useState(false)
  const [busy, setBusy] = useState(false)
  const [confirm, setConfirm] = useState<null | { force: boolean; message?: string }>(null)

  const merge = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      const r = await api.merge(box.repo, box.name, { into: into.trim() || undefined, squash: squash || undefined })
      toasts.ok(`merged ${box.branch} into ${r.into}`, 'remove the box when you are done with it')
    } catch (err) {
      toasts.error(err)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (force: boolean) => {
    setBusy(true)
    try {
      await api.remove(box.repo, box.name, { force, keep_branch: keepBranch })
      toasts.ok(`removed ${box.name}`)
      setConfirm(null)
      onRemoved()
    } catch (err) {
      if (!force && err instanceof ApiError && err.code === 'unmerged_work') {
        setConfirm({ force: true, message: err.message })
      } else {
        toasts.error(err)
        setConfirm(null)
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack">
      <section className="card">
        <h3>Merge</h3>
        <p className="muted">
          Merges <code>{box.branch}</code> into the main checkout's current branch. It refuses when either side has uncommitted
          changes and never switches branches for you.
        </p>
        <form onSubmit={merge} className="form">
          <label>
            Into
            <input value={into} onChange={(e) => setInto(e.target.value)} placeholder="the branch checked out in the main checkout" spellCheck={false} />
          </label>
          <label className="check">
            <input type="checkbox" checked={squash} onChange={(e) => setSquash(e.target.checked)} /> Squash into one commit
          </label>
          <div className="row end">
            <button className="primary" disabled={busy}>
              Merge
            </button>
          </div>
        </form>
      </section>
      <section className="card danger-zone">
        <h3>Remove</h3>
        <p className="muted">Stops the dev server, drops the database copy, frees the ports and deletes the worktree. It asks first if there is unmerged work.</p>
        <label className="check">
          <input type="checkbox" checked={keepBranch} onChange={(e) => setKeepBranch(e.target.checked)} /> Keep the <code>{box.branch}</code> branch
        </label>
        <div className="row end">
          <button className="danger" disabled={busy} onClick={() => setConfirm({ force: false })}>
            Remove box
          </button>
        </div>
      </section>
      {confirm && (
        <ConfirmDialog
          title={confirm.force ? 'Discard unmerged work?' : `Remove ${box.name}?`}
          confirmLabel={confirm.force ? 'Remove anyway' : 'Remove'}
          danger
          busy={busy}
          onCancel={() => setConfirm(null)}
          onConfirm={() => void remove(confirm.force)}
        >
          {confirm.message ? (
            <p>{confirm.message}</p>
          ) : (
            <p>
              This deletes the worktree at <code>{box.dir}</code>.
            </p>
          )}
          <label className="check">
            <input type="checkbox" checked={keepBranch} onChange={(e) => setKeepBranch(e.target.checked)} /> Keep the <code>{box.branch}</code> branch
          </label>
        </ConfirmDialog>
      )}
    </div>
  )
}
