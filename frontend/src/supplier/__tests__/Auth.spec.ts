import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import Auth from '../Auth.vue'
const mocks = vi.hoisted(() => ({
  route: { path: '/login', query: { redirect: '/supplier/portal' } }, replace: vi.fn(),
  settings: vi.fn(), login: vi.fn(), register: vi.fn(), login2FA: vi.fn(), restore: vi.fn(), sendRegistrationCode: vi.fn(), save: vi.fn()
}))
vi.mock('vue-router', () => ({ useRoute: () => mocks.route, useRouter: () => ({ replace: mocks.replace }) }))
vi.mock('../router', () => ({ supplierReturn: (value: unknown) => value === '/supplier' ? value : '/supplier/portal' }))
vi.mock('../api', () => ({ isolatedSupplierAPI: mocks }))
vi.mock('../session', () => ({ saveSupplierSession: mocks.save, supplierSession: { user: { id: 1, email: 'test@example.test' } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
const global = { stubs: { RouterLink: true } }
beforeEach(() => {
  vi.clearAllMocks()
  mocks.route.path = '/login'
  mocks.settings.mockResolvedValue({ supplier_portal: true, registration_enabled: true, email_delivery_configured: true })
  mocks.login.mockResolvedValue({ access_token: 'supplier-test', user: { id: 1, email: 'test@example.test' } })
  mocks.register.mockResolvedValue({ access_token: 'supplier-test', user: { id: 1, email: 'test@example.test' } })
  mocks.restore.mockResolvedValue(undefined)
})
async function credentials() {
  const wrapper = mount(Auth, { global })
  await flushPromises()
  await wrapper.get('#email').setValue('test@example.test')
  await wrapper.get('#password').setValue('test-only-password')
  return wrapper
}
describe('supplier email auth', () => {
  it('logs in and returns only to supplier portal', async () => {
    const wrapper = await credentials()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.login).toHaveBeenCalledWith('test@example.test', 'test-only-password')
    expect(mocks.save).toHaveBeenCalledOnce()
    expect(mocks.replace).toHaveBeenCalledWith('/supplier/portal')
  })
  it('requires email proof in registration without extra account fields', async () => {
    mocks.route.path = '/register'
    const wrapper = await credentials()
    await wrapper.get('#confirm-password').setValue('test-only-password')
    await wrapper.get('#code').setValue('123456')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.register).toHaveBeenCalledWith('test@example.test', 'test-only-password', '123456')
    expect(wrapper.html()).not.toMatch(/promo|invitation|wallet|oauth|passkey/)
  })
  it('completes TOTP without storing a temporary token as a session', async () => {
    mocks.login.mockResolvedValue({ requires_2fa: true, temp_token: 'temporary-test' })
    mocks.login2FA.mockResolvedValue({ access_token: 'supplier-test', user: { id: 1, email: 'test@example.test' } })
    const wrapper = await credentials()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.save).not.toHaveBeenCalled()
    await wrapper.get('#code').setValue('654321')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.login2FA).toHaveBeenCalledWith('temporary-test', '654321')
    expect(mocks.save).toHaveBeenCalledOnce()
  })
  it.each(['turnstile_enabled', 'tencent_captcha_enabled', 'aliyun_captcha_enabled', 'login_agreement_enabled'])('fails closed when %s is required', async key => {
    mocks.settings.mockResolvedValue({ supplier_portal: true, registration_enabled: true, email_delivery_configured: true, [key]: true })
    const wrapper = mount(Auth, { global })
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe('supplier.securityUnsupported')
    expect(mocks.login).not.toHaveBeenCalled()
  })
  it('rejects a consumer endpoint configuration without supplier marker', async () => {
    mocks.settings.mockResolvedValue({ registration_enabled: true })
    const wrapper = mount(Auth, { global })
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe('supplier.securityUnsupported')
  })
  it('blocks email registration without configured mail but still allows existing login', async () => {
    mocks.settings.mockResolvedValue({ supplier_portal: true, registration_enabled: true, email_delivery_configured: false })
    mocks.route.path = '/register'
    const registration = mount(Auth, { global })
    await flushPromises()
    expect(registration.find('form').exists()).toBe(false)
    expect(registration.get('[role="alert"]').text()).toContain('supplier.smtpUnavailable')
    registration.unmount()
    mocks.route.path = '/login'
    const login = await credentials()
    await login.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.login).toHaveBeenCalledOnce()
  })
  it('fails closed when configuration cannot load' , async () => {
    mocks.settings.mockRejectedValue(new Error('offline'))
    const wrapper = mount(Auth, { global })
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe('supplier.configFailed')
  })
})
