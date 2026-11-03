import { beforeEach, describe, expect, it } from 'vitest'
import { clearSupplierSession, saveSupplierSession, SUPPLIER_STORAGE, supplierSession } from '../session'
beforeEach(() => { localStorage.clear(); clearSupplierSession() })
describe('supplier session storage', () => {
  it('never reads or overwrites consumer credentials', () => {
    localStorage.setItem('auth_token', 'consumer-test')
    localStorage.setItem('refresh_token', 'consumer-refresh-test')
    saveSupplierSession({ access_token: 'supplier-test', refresh_token: 'supplier-refresh-test', user: { id: 2, email: 'supplier@example.test' } })
    expect(localStorage.getItem(SUPPLIER_STORAGE.access)).toBe('supplier-test')
    expect(supplierSession.user?.id).toBe(2)
    clearSupplierSession()
    expect(localStorage.getItem('auth_token')).toBe('consumer-test')
    expect(localStorage.getItem('refresh_token')).toBe('consumer-refresh-test')
    expect(localStorage.getItem(SUPPLIER_STORAGE.refresh)).toBeNull()
  })
})
