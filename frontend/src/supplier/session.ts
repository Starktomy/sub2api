import { reactive } from 'vue'

export interface SupplierIdentity { id: number; email: string }
export interface SupplierSession { access_token: string; refresh_token?: string; user?: SupplierIdentity }
export const SUPPLIER_STORAGE = { access: 'supplier_access_token', refresh: 'supplier_refresh_token' } as const
export const supplierSession = reactive({ user: null as SupplierIdentity | null, ready: false })

export function saveSupplierSession(data: SupplierSession) {
  localStorage.setItem(SUPPLIER_STORAGE.access, data.access_token)
  if (data.refresh_token) localStorage.setItem(SUPPLIER_STORAGE.refresh, data.refresh_token)
  else localStorage.removeItem(SUPPLIER_STORAGE.refresh)
  if (data.user) supplierSession.user = { id: data.user.id, email: data.user.email }
}
export function clearSupplierSession() {
  localStorage.removeItem(SUPPLIER_STORAGE.access)
  localStorage.removeItem(SUPPLIER_STORAGE.refresh)
  supplierSession.user = null
}
