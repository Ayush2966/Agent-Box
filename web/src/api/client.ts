import type { ApiErrorBody, BoxView, Created, Finding, Health, MergeResult, RepoView } from './types'

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public step?: string,
    public hint?: string,
  ) {
    super(message)
  }

  get title(): string {
    return this.step ? `${this.step} failed` : 'request failed'
  }
}

export class SignedOut extends Error {
  constructor() {
    super('signed out')
  }
}

export function parseError(status: number, body: unknown): ApiError {
  const e = (body as Partial<ApiErrorBody> | null)?.error
  if (e && typeof e.message === 'string') return new ApiError(status, e.code ?? 'internal', e.message, e.step, e.hint)
  return new ApiError(status, 'internal', `HTTP ${status}`)
}

let signedOutHandler: () => void = () => {}
/** The app registers what to do when the daemon answers 401 (show the sign-in screen). */
export function onSignedOut(fn: () => void) {
  signedOutHandler = fn
}

type As = 'json' | 'text' | 'none'

async function request<T>(method: string, path: string, body?: unknown, as: As = 'json'): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'content-type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  })
  if (res.status === 401) {
    signedOutHandler()
    throw new SignedOut()
  }
  if (!res.ok) {
    let parsed: unknown = null
    try {
      parsed = await res.json()
    } catch {
      /* not JSON */
    }
    throw parseError(res.status, parsed)
  }
  if (as === 'none' || res.status === 204) return undefined as T
  if (as === 'text') return (await res.text()) as T
  return (await res.json()) as T
}

const enc = encodeURIComponent
const boxPath = (repo: string, name: string) => `/v1/repos/${enc(repo)}/boxes/${enc(name)}`

export const api = {
  health: () => request<Health>('GET', '/v1/health'),
  repos: () => request<RepoView[]>('GET', '/v1/repos'),
  boxes: (repo = '') => request<BoxView[]>('GET', `/v1/boxes?repo=${enc(repo)}`),
  box: (repo: string, name: string) => request<BoxView>('GET', boxPath(repo, name)),
  create: (opts: { repo_path: string; name: string; from?: string; no_dev?: boolean }) =>
    request<Created>('POST', '/v1/boxes', opts),
  start: (repo: string, name: string) => request<void>('POST', `${boxPath(repo, name)}/start`, undefined, 'none'),
  stop: (repo: string, name: string) => request<void>('POST', `${boxPath(repo, name)}/stop`, undefined, 'none'),
  merge: (repo: string, name: string, opts: { into?: string; squash?: boolean }) =>
    request<MergeResult>('POST', `${boxPath(repo, name)}/merge`, opts),
  remove: (repo: string, name: string, opts: { force?: boolean; keep_branch?: boolean }) =>
    request<void>(
      'DELETE',
      `${boxPath(repo, name)}?force=${opts.force ? 'true' : 'false'}&keep_branch=${opts.keep_branch ? 'true' : 'false'}`,
      undefined,
      'none',
    ),
  diff: (repo: string, name: string, against: string) =>
    request<string>('GET', `${boxPath(repo, name)}/diff?against=${enc(against)}`, undefined, 'text'),
  doctor: (repo: string, fix: boolean) => request<Finding[]>(fix ? 'POST' : 'GET', `/v1/repos/${enc(repo)}/doctor`),
  refreshDB: (repo: string, terminate: boolean) =>
    request<void>('POST', `/v1/repos/${enc(repo)}/db/refresh?terminate=${terminate ? 'true' : 'false'}`, undefined, 'none'),
  logsURL: (repo: string, name: string, tail: number) => `${boxPath(repo, name)}/logs?follow=true&tail=${tail}`,
  eventsURL: '/v1/events',
}

/** A one-line description of any thrown value, for toasts and inline errors. */
export function describe(e: unknown): string {
  if (e instanceof ApiError) return e.hint ? `${e.message} — ${e.hint}` : e.message
  if (e instanceof Error) return e.message
  return String(e)
}
