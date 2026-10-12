import type { ReactNode } from 'react'
import { Modal } from './bits'

export function ConfirmDialog({
  title,
  children,
  confirmLabel = 'Confirm',
  danger,
  busy,
  onConfirm,
  onCancel,
}: {
  title: string
  children: ReactNode
  confirmLabel?: string
  danger?: boolean
  busy?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Modal title={title} onClose={onCancel}>
      <div className="stack">{children}</div>
      <div className="row end">
        <button onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button className={danger ? 'danger' : 'primary'} onClick={onConfirm} disabled={busy} autoFocus>
          {confirmLabel}
        </button>
      </div>
    </Modal>
  )
}
