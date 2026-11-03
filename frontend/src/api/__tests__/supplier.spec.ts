import { beforeEach, describe, expect, it, vi } from 'vitest'
import { supplierAPI } from '../supplier'

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))

beforeEach(() => vi.clearAllMocks())

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
