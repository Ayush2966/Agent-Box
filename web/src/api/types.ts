export type BoxState = 'creating' | 'ready' | 'running' | 'stopped' | 'removing' | 'failed'

export interface RepoView {
  id: string
  path: string
}

export interface BoxView {
  id: string
  repo: string
  repo_path: string
  name: string
  dir: string
  branch: string
  state: BoxState
  ports: Record<string, number>
  db_name?: string
  db_url?: string
  pid?: number
  last_step?: string
  last_error?: string
  base?: string
  ahead: number
  behind: number
  dirty: number
  env?: Record<string, string>
  created_at: string
  url?: string
}

export interface Created {
  id: string
  repo: string
  name: string
}

export interface MergeResult {
  into: string
}

export interface Finding {
  kind: string
  detail: string
  fixed: boolean
}

export interface Health {
  ok: boolean
  pid: number
  version: string
  api_addr: string
  ui: boolean
}

export interface ApiErrorBody {
  error: { code: string; message: string; step?: string; hint?: string }
}

export type BoxEventType = 'box.created' | 'box.state' | 'box.step' | 'box.removed'

export interface BoxEvent {
  type: BoxEventType
  box: string // "<repo>/<name>"
  data?: Record<string, unknown>
}
