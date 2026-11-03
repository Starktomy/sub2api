import axios from 'axios'
import { clearSupplierSession, saveSupplierSession, supplierSession, SUPPLIER_STORAGE, type SupplierIdentity, type SupplierSession } from './session'
import type { SupplierProfile } from '@/api/supplier'
import type { SupplierResource, SupplierResourceList } from './resourceTypes'

// Fixed same-origin base. Never inherit consumer VITE_API_BASE_URL or client interceptors.
const client = axios.create({ baseURL: '/api/v1', timeout: 30000, withCredentials: false })
const publicClient = axios.create({ baseURL: '/api/v1', timeout: 30000, withCredentials: false })
export interface SupplierSettings {
  supplier_portal: boolean
  registration_enabled: boolean
  email_delivery_configured: boolean
  turnstile_enabled?: boolean
  tencent_captcha_enabled?: boolean
  aliyun_captcha_enabled?: boolean
  login_agreement_enabled?: boolean
}
export interface SecondFactor { requires_2fa: true; temp_token: string }

async function unwrap<T>(request: Promise<{ data: { code: number; data: T; message?: string } }>): Promise<T> {
  const { data } = await request
  if (!data || data.code !== 0) throw new Error(data?.message || 'Request failed')
  return data.data
}
client.interceptors.request.use(config => {
  const token = localStorage.getItem(SUPPLIER_STORAGE.access)
  if (token) config.headers.Authorization = `Bearer ${token}`
  return config
})
let refreshing: Promise<void> | null = null
async function refresh() {
  if (!refreshing) refreshing = (async () => {
    const token = localStorage.getItem(SUPPLIER_STORAGE.refresh)
    if (!token) throw new Error('No supplier refresh token')
    const data = await unwrap<SupplierSession>(publicClient.post('/auth/refresh', { refresh_token: token }))
    saveSupplierSession(data)
  })().catch(error => { clearSupplierSession(); throw error }).finally(() => { refreshing = null })
  await refreshing
}
client.interceptors.response.use(response => response, async error => {
  const config = error.config
  if (error.response?.status === 401 && config && !config._supplierRetry) {
    config._supplierRetry = true
    await refresh()
    return client.request(config)
  }
  if (error.response?.status === 401) clearSupplierSession()
  return Promise.reject(error)
})

export const isolatedSupplierAPI = {
  settings: () => unwrap<SupplierSettings>(publicClient.get('/settings/public')),
  async login(email: string, password: string) {
    return unwrap<SupplierSession | SecondFactor>(publicClient.post('/auth/login', { email, password }))
  },
  login2FA: (tempToken: string, code: string) => unwrap<SupplierSession>(publicClient.post('/auth/login/2fa', { temp_token: tempToken, totp_code: code })),
  register: (email: string, password: string, code: string) => unwrap<SupplierSession>(publicClient.post('/auth/register', { email, password, verify_code: code })),
  sendRegistrationCode: (email: string) => unwrap<unknown>(publicClient.post('/auth/send-verify-code', { email })),
  async restore() {
    try {
      if (!localStorage.getItem(SUPPLIER_STORAGE.access) && localStorage.getItem(SUPPLIER_STORAGE.refresh)) await refresh()
      if (localStorage.getItem(SUPPLIER_STORAGE.access)) supplierSession.user = await unwrap<SupplierIdentity>(client.get('/auth/me'))
    } catch { clearSupplierSession() }
    finally { supplierSession.ready = true }
  },
  async logout() {
    try { await unwrap(publicClient.post('/auth/logout', { refresh_token: localStorage.getItem(SUPPLIER_STORAGE.refresh) })) }
    finally { clearSupplierSession() }
  },
  resources: () => unwrap<SupplierResourceList>(client.get('/supplier/resources')),
  importResource: (displayName: string, content: string) => unwrap<SupplierResource>(client.post('/supplier/resources/import', { display_name: displayName, content })),
  resourceUsage: (id: string, version: number) => unwrap<SupplierResource>(client.post(`/supplier/resources/${id}/usage`, { version })),
  resumeResourceSync: (id: string, version: number) => unwrap<SupplierResource>(client.post(`/supplier/resources/${id}/resume-sync`, { version })),
  pauseResource: (id: string, version: number) => unwrap<SupplierResource>(client.post(`/supplier/resources/${id}/pause`, { version })),
  disconnectResource: (id: string, version: number) => unwrap<SupplierResource>(client.post(`/supplier/resources/${id}/disconnect`, { version })),
  profile: () => unwrap<SupplierProfile>(client.get('/supplier/profile')),
  sendCode: () => unwrap<unknown>(client.post('/supplier/send-verify-code', {})),
  enroll: (displayName: string, code: string) => unwrap<SupplierProfile>(client.post('/supplier/enroll', { display_name: displayName, verify_code: code }))
}

export function supplierErrorReason(error: unknown): string | undefined {
  return (error as { response?: { data?: { reason?: string } } })?.response?.data?.reason
}
