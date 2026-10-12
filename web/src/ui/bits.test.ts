import { afterEach, describe, expect, it, vi } from 'vitest'
import { openExternal } from './bits'

describe('openExternal', () => {
  afterEach(() => {
    delete (globalThis as { window?: unknown }).window
    delete (globalThis as { location?: unknown }).location
  })

  it('opens only http(s) URLs', () => {
    const open = vi.fn()
    ;(globalThis as { window?: unknown }).window = { open }
    ;(globalThis as { location?: unknown }).location = { href: 'http://localhost:7777/ui/' }
    openExternal('https://example.com/x')
    openExternal('http://feature-x.localhost:7777')
    expect(open).toHaveBeenCalledTimes(2)
    openExternal('javascript:alert(1)')
    openExternal('file:///etc/passwd')
    openExternal('data:text/html,hi')
    expect(open).toHaveBeenCalledTimes(2)
  })
})
