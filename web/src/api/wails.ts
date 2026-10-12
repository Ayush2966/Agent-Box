// Detection of the Wails desktop runtime, which the app injects into the window.
export interface WailsRuntime {
  EventsOn(name: string, cb: (data: unknown) => void): () => void
  /** Like EventsOn, but the runtime drops the listener after its first call. */
  EventsOnce(name: string, cb: (data: unknown) => void): () => void
  BrowserOpenURL(url: string): void
}

export interface WailsApp {
  /** Streams a box's log as `agentbox:log:<id>` events; the caller picks the id and listens first. */
  StartLogs(id: string, repo: string, name: string): Promise<void>
  StopLogs(id: string): Promise<void>
  ConnState(): Promise<string>
}

declare global {
  interface Window {
    runtime?: WailsRuntime
    go?: { main?: { App?: WailsApp } }
  }
}

/** The Wails runtime and bound app, or null in a plain browser. */
export function wails(): { runtime: WailsRuntime; app: WailsApp } | null {
  if (typeof window === 'undefined') return null
  const runtime = window.runtime
  const app = window.go?.main?.App
  return runtime && app ? { runtime, app } : null
}
