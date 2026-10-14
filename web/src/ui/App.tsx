import { useCallback, useEffect, useMemo, useState } from 'react'
import { SignedOut, api, onSignedOut } from '../api/client'
import { connectSSE, type SSEState } from '../api/sse'
import type { BoxEvent, BoxEventType, Health, RepoView } from '../api/types'
import { boxes, selectBoxes, splitId, useBoxes } from '../store/boxes'
import { filterRepos, groupRepos, summarizeRepos } from '../store/repos'
import { navigate, useRoute } from '../store/route'
import { toasts } from '../store/toasts'
import { AllBoxes } from './AllBoxes'
import { BoxPage } from './BoxPage'
import { BoxTable } from './BoxTable'
import { DoctorPanel } from './DoctorPanel'
import { NewBoxDialog } from './NewBoxDialog'
import { RepoHeader } from './RepoHeader'
import { Sidebar } from './Sidebar'
import { SignIn } from './SignIn'
import { Toasts } from './Toasts'
import { TopBar } from './TopBar'
import { useKey } from './bits'
import { ConfirmDialog } from './confirm'

type Auth = 'checking' | 'in' | 'out'
type Dialog = 'none' | 'new' | 'doctor' | 'refresh'
const EVENTS: BoxEventType[] = ['box.created', 'box.state', 'box.step', 'box.removed']

export function App() {
  const [auth, setAuth] = useState<Auth>('checking')
  const [health, setHealth] = useState<Health | null>(null)
  const [repos, setRepos] = useState<RepoView[]>([])
  const [sse, setSse] = useState<SSEState>('connecting')
  const [dialog, setDialog] = useState<Dialog>('none')
  const [navOpen, setNavOpen] = useState(false)
  const [query, setQuery] = useState('')
  const route = useRoute()
  const state = useBoxes()

  const load = useCallback(async () => {
    try {
      const [h, rs, bs] = await Promise.all([api.health(), api.repos(), api.boxes()])
      setHealth(h)
      setRepos(rs)
      boxes.dispatch({ type: 'load', boxes: bs })
      setAuth('in')
    } catch (e) {
      if (e instanceof SignedOut) setAuth('out')
      else toasts.error(e)
    }
  }, [])

  useEffect(() => {
    onSignedOut(() => setAuth('out'))
    void load()
  }, [load])

  useEffect(() => {
    if (auth !== 'in') return
    return connectSSE({
      url: api.eventsURL,
      events: EVENTS,
      onState: setSse,
      onReconnect: () => void load(),
      onEvent: (type, data) => {
        const ev = { ...(data as BoxEvent), type: type as BoxEventType }
        boxes.dispatch({ type: 'event', event: ev })
        if (type === 'box.created' || type === 'box.state') {
          const { repo, name } = splitId(ev.box)
          api
            .box(repo, name)
            .then((b) => boxes.dispatch({ type: 'upsert', box: b }))
            .catch(() => {
              /* removed in the meantime */
            })
        }
        if (type === 'box.created') {
          api.repos().then(setRepos).catch(() => {})
        }
      },
    })
  }, [auth, load])

  const summaries = useMemo(() => summarizeRepos(repos, Object.values(state.byId)), [repos, state.byId])
  const groups = useMemo(() => groupRepos(filterRepos(summaries, query)), [summaries, query])
  const repoId = route.kind === 'home' ? (summaries[0]?.id ?? '') : route.kind === 'all' ? '' : route.repo
  const repo = summaries.find((r) => r.id === repoId) ?? (route.kind === 'all' ? summaries[0] : undefined)
  const closeDialog = useCallback(() => setDialog('none'), [])
  const closeNav = useCallback(() => setNavOpen(false), [])
  useKey('n', () => setDialog('new'), auth === 'in' && dialog === 'none' && summaries.length > 0)
  useEffect(() => {
    if (!navOpen) return
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setNavOpen(false)
    }
    addEventListener('keydown', h)
    return () => removeEventListener('keydown', h)
  }, [navOpen])

  if (auth === 'out') return <SignIn />

  return (
    <div className="app">
      <TopBar health={health} sse={sse} navOpen={navOpen} onToggleNav={() => setNavOpen((o) => !o)} />
      <div className="body">
        <Sidebar
          groups={groups}
          total={summaries.length}
          boxes={Object.keys(state.byId).length}
          active={route.kind === 'all' ? 'all' : repoId}
          query={query}
          onQuery={setQuery}
          open={navOpen}
          onClose={closeNav}
          health={health}
        />
        <main className="main">
          {auth === 'checking' && <p className="muted">Loading…</p>}
          {auth === 'in' && repos.length === 0 && (
            <div className="empty card">
              <p className="strong">agentbox doesn't know any repos yet.</p>
              <p className="muted">In a repo you want to work on:</p>
              <pre className="copy">cd your-repo{'\n'}agentbox init{'\n'}agentbox new feature-x</pre>
            </div>
          )}
          {auth === 'in' && repos.length > 0 && !repo && <p className="muted">Unknown repo. Pick one on the left.</p>}
          {auth === 'in' && route.kind === 'all' && <AllBoxes boxes={Object.values(state.byId)} steps={state.steps} initialQuery={route.q} onNew={() => setDialog('new')} />}
          {auth === 'in' && repo && route.kind !== 'box' && route.kind !== 'all' && (
            <>
              <RepoHeader repo={repo} onNew={() => setDialog('new')} onDoctor={() => setDialog('doctor')} onRefreshDB={() => setDialog('refresh')} />
              <BoxTable boxes={selectBoxes(state, repo.id)} steps={state.steps} repo={repo} onOpen={(b) => navigate({ kind: 'box', repo: b.repo, name: b.name })} />
            </>
          )}
          {auth === 'in' && route.kind === 'box' && (
            <BoxPage repo={route.repo} name={route.name} onBack={() => navigate({ kind: 'repo', repo: route.repo })} />
          )}
        </main>
      </div>
      {dialog === 'new' && summaries.length > 0 && <NewBoxDialog repo={route.kind === 'all' ? undefined : repo} repos={summaries} onClose={closeDialog} />}
      {dialog === 'doctor' && repo && <DoctorPanel repo={repo} onClose={closeDialog} />}
      {dialog === 'refresh' && repo && <RefreshDialog repo={repo} onClose={closeDialog} />}
      <Toasts />
    </div>
  )
}

function RefreshDialog({ repo, onClose }: { repo: RepoView; onClose: () => void }) {
  const [terminate, setTerminate] = useState(false)
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      await api.refreshDB(repo.id, terminate)
      toasts.ok('database template refreshed', 'new boxes start from the current source database')
      onClose()
    } catch (e) {
      toasts.error(e)
      setBusy(false)
    }
  }
  return (
    <ConfirmDialog title="Refresh the database template?" confirmLabel="Refresh" busy={busy} onCancel={onClose} onConfirm={() => void go()}>
      <p>Re-snapshots the source database so that new boxes start from its current contents. Existing boxes keep their copies.</p>
      <label className="check">
        <input type="checkbox" checked={terminate} onChange={(e) => setTerminate(e.target.checked)} /> Terminate stray connections to the template (Postgres)
      </label>
    </ConfirmDialog>
  )
}
