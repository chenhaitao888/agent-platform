import { describe, expect, it } from 'vitest'
import { loopbackAPITarget } from '../dev-proxy'

describe('development API proxy', () => {
  it('preserves the default and supports an isolated pilot port', () => {
    expect(loopbackAPITarget(undefined)).toBe('http://127.0.0.1:8080')
    expect(loopbackAPITarget('http://127.0.0.1:8081')).toBe('http://127.0.0.1:8081')
  })

  it.each([
    'https://127.0.0.1:8081', 'http://example.com', 'http://0.0.0.0:8081',
    'http://user:secret@127.0.0.1:8081', 'http://127.0.0.1:8081/path',
    'http://127.0.0.1:8081?token=secret', 'http://127.0.0.1:8081#fragment',
  ])('rejects unsupported proxy target %s', (target) => {
    expect(() => loopbackAPITarget(target)).toThrow()
  })
})
