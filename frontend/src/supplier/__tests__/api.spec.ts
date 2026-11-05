import axios, { AxiosError, AxiosHeaders, type AxiosInstance, type InternalAxiosRequestConfig } from 'axios'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { clearSupplierSession, SUPPLIER_STORAGE } from '../session'

let api: typeof import('../api').isolatedSupplierAPI
const requests: InternalAxiosRequestConfig[] = []
let expired = false
beforeAll(async () => {
  const create = axios.create.bind(axios)
  const spy = vi.spyOn(axios, 'create').mockImplementation(config => {
    const instance: AxiosInstance = create(config)
    instance.defaults.adapter = async request => {
      requests.push(request)
      if (expired && request.url === '/auth/me' && request.headers.Authorization === 'Bearer old-supplier') {
        throw new AxiosError('Expired', 'ERR_BAD_REQUEST', request, undefined, { status: 401, statusText: 'Unauthorized', headers: new AxiosHeaders(), config: request, data: {} })
      }
      const data = request.url === '/auth/refresh'
        ? { access_token: 'new-supplier', refresh_token: 'new-refresh' }
        : request.url === '/auth/me' ? { id: 4, email: 'supplier@example.test' } : {}
      return { data: { code: 0, data }, status: 200, statusText: 'OK', headers: new AxiosHeaders(), config: request }
    }
    return instance
  })
  api = (await import('../api')).isolatedSupplierAPI
  spy.mockRestore()
})
beforeEach(() => { localStorage.clear(); clearSupplierSession(); requests.length = 0; expired = false })
describe('isolated supplier transport', () => {
  it('uses fixed same-origin base and never attaches consumer credentials', async () => {
    localStorage.setItem('auth_token', 'consumer-test')
    await api.settings()
    expect(requests[0].baseURL).toBe('/api/v1')
    expect(requests[0].headers.Authorization).toBeUndefined()
    expect(requests[0].withCredentials).toBe(false)
    await api.profile()
    expect(requests[1].headers.Authorization).toBeUndefined()
  })
  it('refreshes and retries only with supplier credentials', async () => {
    localStorage.setItem('auth_token', 'consumer-test')
    localStorage.setItem('refresh_token', 'consumer-refresh-test')
    localStorage.setItem(SUPPLIER_STORAGE.access, 'old-supplier')
    localStorage.setItem(SUPPLIER_STORAGE.refresh, 'supplier-refresh-test')
    expired = true
    await api.restore()
    const refresh = requests.find(request => request.url === '/auth/refresh')!
    expect(JSON.parse(refresh.data)).toEqual({ refresh_token: 'supplier-refresh-test' })
    expect(refresh.headers.Authorization).toBeUndefined()
    expect(requests[requests.length - 1]?.headers.Authorization).toBe('Bearer new-supplier')
    expect(localStorage.getItem('auth_token')).toBe('consumer-test')
    expect(localStorage.getItem(SUPPLIER_STORAGE.access)).toBe('new-supplier')
  })
  it('clears only supplier credentials when refresh is unavailable', async () => {
    localStorage.setItem('auth_token', 'consumer-test')
    localStorage.setItem(SUPPLIER_STORAGE.access, 'old-supplier')
    expired = true
    await api.restore()
    expect(localStorage.getItem(SUPPLIER_STORAGE.access)).toBeNull()
    expect(localStorage.getItem('auth_token')).toBe('consumer-test')
  })
  it('imports explicitly consented supply only to the isolated supplier endpoint', async () => {
    await api.importResource({ display_name: 'My Codex', content: '{"accessToken":"synthetic-test-only"}', consent_version: 'all_available_continuous_v1' })
    expect(requests[0].url).toBe('/supplier/resources/import')
    expect(JSON.parse(requests[0].data)).toEqual({ display_name: 'My Codex', content: '{"accessToken":"synthetic-test-only"}', consent_version: 'all_available_continuous_v1' })
  })
  it('updates the original resource with explicit consent and its optimistic version', async () => {
    const payload = { display_name: 'My Codex', content: '{"accessToken":"synthetic-test-only"}', consent_version: 'all_available_continuous_v1' as const, resource_id: 'resource-test-id', resource_version: 4 }
    await api.importResource(payload)
    expect(requests).toHaveLength(1)
    expect(requests[0].url).toBe('/supplier/resources/import')
    expect(JSON.parse(requests[0].data)).toEqual(payload)
    expect(JSON.parse(requests[0].data)).not.toHaveProperty('authorization_expires_at')
  })
  it('uses resource ID and optimistic version without selecting an owner', async () => {
    await api.resourceUsage('resource-test-id', 3)
    await api.disconnectResource('resource-test-id', 3)
    await api.startResource('resource-test-id', 3)
    await api.pauseResource('resource-test-id', 3)
    expect(requests[0].url).toBe('/supplier/resources/resource-test-id/usage')
    expect(requests[1].url).toBe('/supplier/resources/resource-test-id/disconnect')
    expect(requests[2].url).toBe('/supplier/resources/resource-test-id/start')
    expect(requests[3].url).toBe('/supplier/resources/resource-test-id/pause')
    for (const request of requests) expect(JSON.parse(request.data)).toEqual({ version: 3 })
  })
  it('reads owner statistics through supplier credentials without owner parameters', async () => {
    localStorage.setItem('auth_token', 'consumer-test')
    localStorage.setItem(SUPPLIER_STORAGE.access, 'supplier-test')
    await api.resourceStats('resource/id')
    expect(requests[0].url).toBe('/supplier/resources/resource%2Fid/stats')
    expect(requests[0].method).toBe('get')
    expect(requests[0].headers.Authorization).toBe('Bearer supplier-test')
    expect(requests[0].params).toBeUndefined()
  })
  it('only sends email proof and name in enrollment', async () => {
    await api.enroll('Supplier', '123456')
    expect(JSON.parse(requests[0].data)).toEqual({ display_name: 'Supplier', verify_code: '123456' })
  })
})
