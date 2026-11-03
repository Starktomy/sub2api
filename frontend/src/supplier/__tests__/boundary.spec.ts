import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import router, { supplierReturn, SUPPLIER_ROUTES } from '../router'
import { supplierSession } from '../session'

const sources = import.meta.glob('../*.{ts,vue}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
describe('supplier entry boundary', () => {
  it('only allows supplier return destinations', () => {
    expect(supplierReturn('/supplier')).toBe('/supplier')
    expect(supplierReturn('/supplier/portal')).toBe('/supplier/portal')
    for (const path of ['/admin', '/dashboard', '//other.test', '/oauth/mcp/authorize', '/supplier/portal?redirect=/admin']) expect(supplierReturn(path)).toBe('/supplier/portal')
    expect(SUPPLIER_ROUTES).toEqual(['/supplier', '/supplier/portal', '/login', '/register'])
  })
  it('redirects anonymous portal visits to supplier login and unknown routes to entry', async () => {
    supplierSession.ready = true
    supplierSession.user = null
    await router.push('/supplier/portal')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.redirect).toBe('/supplier/portal')
    await router.push('/admin/dashboard')
    expect(router.currentRoute.value.path).toBe('/supplier')
  })
  it('does not import shared roots, consumer stores or auth clients', () => {
    for (const text of Object.values(sources)) {
      expect(text).not.toMatch(/from ['"]@\/(stores|router|api\/(client|auth)|views)\b/)
      expect(text).not.toMatch(/\/admin\/|\/dashboard|passkey|oauth|subscriptions|announcements/)
    }
  })
  it('builds a separate entry and enforces module graph boundary', () => {
    const config = readFileSync(resolve(process.cwd(), 'vite.supplier.config.ts'), 'utf8')
    expect(config).toContain("input: resolve(__dirname, 'supplier.html')")
    expect(config).toContain('this.getModuleIds()')
    expect(config).toContain("html.fileName = 'index.html'")
    expect(config).toContain('Consumer module in supplier build')
    expect(config).not.toContain('backend/internal/web/dist')
  })
})
