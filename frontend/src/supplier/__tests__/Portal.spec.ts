import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import SupplierPortalView from '../Portal.vue'
import SupplierEntryView from '../Entry.vue'

enableAutoUnmount(afterEach)

const mocks = vi.hoisted(() => ({
  auth: { isAuthenticated: true, user: { username: 'Existing name', email: 'supplier@example.test' } },
  settings: vi.fn(), getProfile: vi.fn(), enroll: vi.fn(), sendVerifyCode: vi.fn()
}))
vi.mock('../session', () => ({ supplierSession: mocks.auth }))
vi.mock('../api', () => ({ isolatedSupplierAPI: { settings: mocks.settings, profile: mocks.getProfile, enroll: mocks.enroll, sendCode: mocks.sendVerifyCode }, supplierErrorReason: (err: { reason?: string; code?: string }) => err.reason || err.code }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const global = { stubs: { Resources: { template: '<div>supplier.resourcesTitle supplier.noResources</div>' }, RouterLink: { props: ['to'], template: '<a :data-to="JSON.stringify(to)"><slot /></a>' } } }
const profile = { id: 1, display_name: 'My supplier', verified_email: 'supplier@example.test' }

beforeEach(() => {
  vi.clearAllMocks()
  mocks.auth.isAuthenticated = true
  mocks.auth.user = { username: 'Existing name', email: 'supplier@example.test' }
  mocks.settings.mockResolvedValue({ email_delivery_configured: true })
  mocks.getProfile.mockRejectedValue({ status: 404, reason: 'NOT_ENROLLED' })
  mocks.enroll.mockResolvedValue(profile)
  mocks.sendVerifyCode.mockResolvedValue(undefined)
})

async function enrollmentForm() {
  const wrapper = mount(SupplierPortalView, { global })
  await flushPromises()
  await wrapper.get('#supplier-name').setValue('  My supplier  ')
  await wrapper.get('#supplier-code').setValue('123456')
  return wrapper
}

describe('supplier portal', () => {
  it('offers existing email auth without fetching a profile to anonymous visitors', () => {
    mocks.auth.isAuthenticated = false
    mocks.auth.user = null as unknown as typeof mocks.auth.user
    const wrapper = mount(SupplierEntryView, { global })
    const links = wrapper.findAll('a').map(link => link.attributes('data-to'))
    expect(links).toContain(JSON.stringify({ path: '/register', query: { redirect: '/supplier/portal' } }))
    expect(links).toContain(JSON.stringify({ path: '/login', query: { redirect: '/supplier/portal' } }))
    expect(mocks.getProfile).not.toHaveBeenCalled()
  })

  it('does not treat arbitrary failures as permission to enroll', async () => {
    mocks.getProfile.mockRejectedValue({ status: 503 })
    const wrapper = mount(SupplierPortalView, { global })
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe('supplier.loadFailed')
  })

  it('requires email reverification instead of showing stale profile data', async () => {
    mocks.getProfile.mockRejectedValue({ status: 403, reason: 'SUPPLIER_EMAIL_VERIFICATION_REQUIRED' })
    const wrapper = mount(SupplierPortalView, { global })
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(true)
    expect(wrapper.get('[role="alert"]').text()).toBe('supplier.reverify')
  })

  it('blocks enrollment code sending when mail is not configured', async () => {
    mocks.settings.mockResolvedValue({ email_delivery_configured: false })
    const wrapper = mount(SupplierPortalView, { global })
    await flushPromises()
    expect(wrapper.text()).toContain('supplier.smtpUnavailable')
    expect(wrapper.get('button[type="button"]').attributes('disabled')).toBeDefined()
    expect(mocks.sendVerifyCode).not.toHaveBeenCalled()
  })

  it('enrolls only the signed-in user with display name and verification code', async () => {
    const wrapper = await enrollmentForm()
    expect(wrapper.get('#supplier-name').element).toBeTruthy()
    await wrapper.get('button[type="button"]').trigger('click')
    await flushPromises()
    expect(mocks.sendVerifyCode).toHaveBeenCalledWith()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.enroll).toHaveBeenCalledWith('My supplier', '123456')
    expect(wrapper.text()).toContain('supplier.resourcesTitle')
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('accepts an idempotent enrollment response from a concurrent tab', async () => {
    const wrapper = await enrollmentForm()
    mocks.enroll.mockResolvedValue(profile)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.getProfile).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('My supplier')
    expect(wrapper.text()).toContain('supplier.noResources')
  })

  it('prevents duplicate code sends during the email cooldown', async () => {
    const wrapper = await enrollmentForm()
    const send = wrapper.get('button[type="button"]')
    await send.trigger('click')
    await flushPromises()
    expect(send.attributes('disabled')).toBeDefined()
    await send.trigger('click')
    expect(mocks.sendVerifyCode).toHaveBeenCalledTimes(1)
  })

  it('keeps verification failures actionable without inventing a profile', async () => {
    const wrapper = await enrollmentForm()
    mocks.enroll.mockRejectedValue({ status: 400, code: 'INVALID_VERIFY_CODE' })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('supplier.enrollFailed')
    expect(wrapper.find('form').exists()).toBe(true)
  })
})
