import { wails } from './wails'

/** Copies text: through the Wails runtime inside the app, the Clipboard API in a browser. */
export async function copyText(text: string): Promise<void> {
  const w = wails()
  if (w?.runtime.ClipboardSetText) {
    await w.runtime.ClipboardSetText(text)
    return
  }
  if (typeof navigator !== 'undefined' && navigator.clipboard) {
    await navigator.clipboard.writeText(text)
    return
  }
  throw new Error('clipboard unavailable')
}
