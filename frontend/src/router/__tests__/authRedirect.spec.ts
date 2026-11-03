import { describe, expect, it } from 'vitest'
import { authDestination, safeAuthRedirect } from '../authRedirect'

describe('email authentication destinations', () => {
  it('preserves the supplier portal for login and registration', () => {
    for (const path of ['/login', '/register'] as const) {
      expect(authDestination(path, '/supplier/portal')).toEqual({
        path, query: { redirect: '/supplier/portal' }
      })
    }
  })

  it.each([undefined, ['//evil.test'], '//evil.test', 'https://evil.test', '/\\evil.test', '/foo\nbar', '/foo://bar'])('rejects unsafe destination %s', (value) => {
    expect(safeAuthRedirect(value)).toBe('/dashboard')
  })

  it('preserves local query and hash', () => {
    expect(safeAuthRedirect('/supplier/portal?from=email#profile')).toBe('/supplier/portal?from=email#profile')
  })
})
