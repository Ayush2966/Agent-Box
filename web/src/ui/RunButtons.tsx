import { useState } from 'react'
import { api } from '../api/client'
import type { BoxView } from '../api/types'
import { toasts } from '../store/toasts'

/** Start/Stop for one box, with an in-flight guard. */
export function RunButtons({ box }: { box: BoxView }) {
  const [busy, setBusy] = useState(false)
  const act = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true)
    try {
      await fn()
      toasts.ok(done)
    } catch (e) {
      toasts.error(e)
    } finally {
      setBusy(false)
    }
  }
  if (box.state === 'running') {
    return (
      <button disabled={busy} onClick={() => act(() => api.stop(box.repo, box.name), `stopped ${box.name}`)}>
        Stop
      </button>
    )
  }
  if (box.state === 'ready' || box.state === 'stopped') {
    return (
      <button disabled={busy} onClick={() => act(() => api.start(box.repo, box.name), `started ${box.name}`)}>
        Start
      </button>
    )
  }
  return null
}
