import { toasts } from '../store/toasts'

export function Toasts() {
  const list = toasts.use()
  return (
    <div className="toasts" aria-live="polite">
      {list.map((t) => (
        <div key={t.id} className={`toast toast-${t.kind}`}>
          <div>
            <strong>{t.title}</strong>
            {t.message && <div>{t.message}</div>}
            {t.hint && <div className="muted">hint: {t.hint}</div>}
          </div>
          <button className="icon" aria-label="Dismiss" onClick={() => toasts.dismiss(t.id)}>
            ×
          </button>
        </div>
      ))}
    </div>
  )
}
