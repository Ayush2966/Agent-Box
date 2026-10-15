import { Fragment, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { ApiError, api } from '../api/client'
import type { BoxView } from '../api/types'
import { boxes, useBoxes } from '../store/boxes'
import { toasts } from '../store/toasts'
import { DiffView } from './DiffView'
import { LogView } from './LogView'
import { RunButtons } from './RunButtons'
import { CopyButton, ExternalLink, StatePill, formatPorts } from './bits'
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
        <button className="quiet back" onClick={onBack}>
          ← boxes
        </button>
        <h1 className="mono">{box.name}</h1>
        <StatePill state={box.state} />
        {state.steps[id] && <span className="muted">{state.steps[id]}…</span>}
        <span className="spacer" />
        <CopyButton text={box.dir} label="Copy path" title={`Copy ${box.dir}`} />
        {box.url && (
          <ExternalLink className="button" href={box.url} title="Opens in your browser">
            Open app ↗
          </ExternalLink>
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

function humanBytes(n: number): string {
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

function DiskUsage({ box }: { box: BoxView }) {
  const [size, setSize] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  if (size !== null) return <>{humanBytes(size)}</>
  return (
    <button
      className="quiet small"
      disabled={busy}
      onClick={() => {
        setBusy(true)
        api
          .disk(box.repo, box.name)
          .then((d) => setSize(d.bytes))
          .catch(toasts.error)
          .finally(() => setBusy(false))
      }}
    >
      {busy ? 'measuring…' : 'measure'}
    </button>
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
        <ExternalLink href={box.url}>{box.url}</ExternalLink>
      ) : (
        '—'
      ),
    ],
    ['database', box.db_name ?? '—'],
    ['disk', <DiskUsage box={box} />],
    ['pid', box.pid ?? '—'],
    ['created', new Date(box.created_at).toLocaleString()],
  ]
  if (box.last_error) rows.push(['last error', <span className="error">{box.last_error}</span>])
  const env = Object.entries(box.env ?? {}).sort(([a], [b]) => a.localeCompare(b))
  const cd = `cd ${JSON.stringify(box.dir)}`
  return (
    <div className="stack">
      <section className="card agent-card">
        <div className="row">
          <h3>Point your agent here</h3>
          <span className="spacer" />
          <OpenButtons box={box} />
          <CopyButton text={cd} label="Copy cd command" />
          <CopyButton text={box.dir} label="Copy path" />
        </div>
        <pre className="copy block">{cd}</pre>
        <p className="muted small">
          Its own worktree on <code>{box.branch}</code>
          {box.base ? <> from <code>{box.base}</code></> : null}
          {box.url ? (
            <>
              ; the app answers at <ExternalLink href={box.url}>{box.url}</ExternalLink>
            </>
          ) : null}
          .
        </p>
      </section>
      <section className="card">
        <h3>Details</h3>
        <dl className="facts">
          {rows.map(([k, v]) => (
            <Fragment key={k}>
              <dt>{k}</dt>
              <dd>{v}</dd>
            </Fragment>
          ))}
        </dl>
      </section>
      {env.length > 0 && (
        <section className="card">
          <h3>Managed env</h3>
          <p className="muted small">Written into the box's env file and exported to its setup and dev commands.</p>
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
        </section>
      )}
    </div>
  )
}

/** Editor / terminal / file manager on the box's directory, via the daemon. */
function OpenButtons({ box }: { box: BoxView }) {
  const [busy, setBusy] = useState<string | null>(null)
  const open = async (app: 'editor' | 'terminal' | 'files') => {
    setBusy(app)
    try {
      const r = await api.open(box.repo, box.name, app)
      toasts.ok(`opened in ${r.launched}`)
    } catch (e) {
      toasts.error(e)
    } finally {
      setBusy(null)
    }
  }
  return (
    <>
      <button className="primary" disabled={busy !== null} onClick={() => open('editor')} title="Open the box in your editor (set editor: in ~/.agentbox/config.yaml to choose)">
        Open in editor
      </button>
      <button disabled={busy !== null} onClick={() => open('terminal')} title="A terminal window in the box's directory">
        Terminal
      </button>
      <button className="quiet" disabled={busy !== null} onClick={() => open('files')} title="Show the box's directory in the file manager">
        Reveal
      </button>
    </>
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
