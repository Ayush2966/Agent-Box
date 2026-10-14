import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

describe('copyText', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('prefers the Wails runtime inside the app', async () => {
    const ClipboardSetText = vi.fn(() => Promise.resolve())
    vi.stubGlobal('window', { runtime: { ClipboardSetText, EventsOn() {}, EventsOnce() {}, BrowserOpenURL() {} }, go: { main: { App: {} } } })
    const writeText = vi.fn()
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    await copyText('hi')
    expect(ClipboardSetText).toHaveBeenCalledWith('hi')
    expect(writeText).not.toHaveBeenCalled()
  })

  it('uses the Clipboard API in a browser and fails loudly without one', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    vi.stubGlobal('window', {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    await copyText('x')
    expect(writeText).toHaveBeenCalledWith('x')
    vi.stubGlobal('navigator', {})
    await expect(copyText('x')).rejects.toThrow('clipboard unavailable')
  })
})
