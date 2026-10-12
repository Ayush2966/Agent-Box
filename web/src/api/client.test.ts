import { describe, expect, it } from 'vitest'
import { ApiError, parseError } from './client'

describe('parseError', () => {
  it('reads the daemon error shape', () => {
    const e = parseError(409, { error: { code: 'unmerged_work', message: 'box a has 2 unmerged commits', hint: 'merge first' } })
    expect(e).toBeInstanceOf(ApiError)
    expect(e.status).toBe(409)
    expect(e.code).toBe('unmerged_work')
    expect(e.message).toBe('box a has 2 unmerged commits')
    expect(e.hint).toBe('merge first')
    expect(e.title).toBe('request failed')
  })

  it('names the step in the title', () => {
    expect(parseError(500, { error: { code: 'internal', message: 'x', step: 'db clone' } }).title).toBe('db clone failed')
  })

  it('falls back when the body is not the error shape', () => {
    const e = parseError(502, 'Bad Gateway')
    expect(e.code).toBe('internal')
    expect(e.message).toBe('HTTP 502')
  })
})
