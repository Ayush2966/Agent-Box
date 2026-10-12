import { useEffect, type ReactNode } from 'react'
import { wails } from '../api/wails'

export function StatePill({ state }: { state: string }) {
  return <span className={`pill pill-${state}`}>{state}</span>
}

export function formatPorts(p: Record<string, number>): string {
  const keys = Object.keys(p).sort()
  return keys.length ? keys.map((k) => `${k}=${p[k]}`).join(' ') : '—'
}

export function basename(p: string): string {
  const parts = p.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? p
}

/** Runs fn when `key` is pressed while no text field has focus. */
export function useKey(key: string, fn: () => void, enabled = true) {
  useEffect(() => {
    if (!enabled) return
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      const typing = !!t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)
      if (e.key === key && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault()
        fn()
      }
    }
    addEventListener('keydown', h)
    return () => removeEventListener('keydown', h)
  }, [key, fn, enabled])
}

export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    addEventListener('keydown', h)
    return () => removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div
      className="backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className={wide ? 'modal wide' : 'modal'} role="dialog" aria-modal="true" aria-label={title}>
        <header className="modal-head">
          <h2>{title}</h2>
          <button className="icon" aria-label="Close" onClick={onClose}>
            ×
          </button>
        </header>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  )
}

/** Opens a URL in the user's browser: via Wails inside the app, a new tab otherwise. */
export function openExternal(url: string) {
  try {
    const u = new URL(url, location.href)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return
  } catch {
    return
  }
  const w = wails()
  if (w) w.runtime.BrowserOpenURL(url)
  else window.open(url, '_blank', 'noopener')
}

/** An anchor that opens outside the app window. */
export function ExternalLink({ href, className, children }: { href: string; className?: string; children: ReactNode }) {
  return (
    <a
      className={className}
      href={href}
      target="_blank"
      rel="noreferrer"
      onClick={(e) => {
        e.preventDefault()
        openExternal(href)
      }}
    >
      {children}
    </a>
  )
}
