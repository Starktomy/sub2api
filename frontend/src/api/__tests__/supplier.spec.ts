import { beforeEach, describe, expect, it, vi } from 'vitest'
import { supplierAPI } from '../supplier'
import { listSupplierResources, setSupplierSupplyControl } from '../admin/suppliers'

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))

beforeEach(() => vi.clearAllMocks())

describe('administrator supplier supply control', () => {
  it('sends only the optimistic version and independent prohibition flag', async () => {
    const resource = { id: 'resource/id', version: 5, admin_disabled: true, supply_enabled: false }
    client.put.mockResolvedValue({ data: resource })
    expect(await setSupplierSupplyControl('resource/id', 4, true)).toEqual(resource)
    expect(client.put).toHaveBeenCalledWith('/admin/supplier/resources/resource%2Fid/supply-control', { version: 4, disabled: true })
    await setSupplierSupplyControl('resource/id', 5, false)
    expect(client.put).toHaveBeenLastCalledWith('/admin/supplier/resources/resource%2Fid/supply-control', { version: 5, disabled: false })
  })

  it('reads credential-free inventory from the administrator endpoint', async () => {
    client.get.mockResolvedValue({ data: { items: null } })
    expect(await listSupplierResources()).toEqual([])
    expect(client.get).toHaveBeenCalledWith('/admin/supplier/resources')
  })
})

describe('supplier API identity boundary', () => {
  it('loads the signed-in profile without selecting a user', async () => {
    client.get.mockResolvedValue({ data: { id: 7 } })
    expect(await supplierAPI.getProfile()).toEqual({ id: 7 })
    expect(client.get).toHaveBeenCalledWith('/supplier/profile')
  })

  it('sends codes only to the server-selected account email', async () => {
    client.post.mockResolvedValue({ data: { message: 'sent' } })
    await supplierAPI.sendVerifyCode()
    expect(client.post).toHaveBeenCalledWith('/supplier/send-verify-code', {})
  })

  it('submits only enrollment fields and accepts an idempotent existing profile', async () => {
    const profile = { id: 7, display_name: 'Existing supplier' }
    client.post.mockResolvedValue({ data: profile })
    expect(await supplierAPI.enroll('Existing supplier', '123456')).toEqual(profile)
    expect(client.post).toHaveBeenCalledWith('/supplier/enroll', {
      display_name: 'Existing supplier', verify_code: '123456'
    })
  })
})
