import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import KiroImportModal from '../KiroImportModal.vue'

const { auth, importAccount } = vi.hoisted(() => ({ auth: { isAdmin: true, token: 'synthetic-session' }, importAccount: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('en') }) }))
vi.mock('@/api/admin/kiro', async importOriginal => ({ ...await importOriginal<typeof import('@/api/admin/kiro')>(), importKiroAccount: importAccount }))
const mounted: ReturnType<typeof mount>[] = []
function render() {
  const wrapper = mount(KiroImportModal, { props: { show: true }, global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } } })
  mounted.push(wrapper)
  return wrapper
}
async function fill(wrapper: ReturnType<typeof render>, secret = '{"refreshToken":"synthetic-secret-not-a-credential"}') {
  await wrapper.get('[data-testid="kiro-name"]').setValue('Test account')
  await wrapper.get('[data-testid="kiro-user-agent"]').setValue('Legitimate client')
  await wrapper.get('[data-testid="kiro-amz-user-agent"]').setValue('Legitimate sdk')
  await wrapper.get('[data-testid="kiro-secret"]').setValue(secret)
}
beforeEach(() => { auth.isAdmin = true; importAccount.mockReset() })
afterEach(() => { mounted.splice(0).forEach(wrapper => wrapper.unmount()); vi.restoreAllMocks() })

describe('admin native Kiro import', () => {
  it('does not render for non-admins', () => { auth.isAdmin = false; expect(render().find('form').exists()).toBe(false) })
  it.each(['not json secret', '[]', 'null', '{"endpoint":"https://injected.invalid","refreshToken":"secret"}', '{"proxyUrl":"secret"}', '{"kiroApiKey":"secret"}', '{"unexpected":"secret"}', '{"refreshToken":"synthetic","email":{"endpoint":"secret"}}', '{"refreshToken":"synthetic","expiresAt":null}'])('rejects malformed/unsupported JSON without echo: %s', async raw => {
    const wrapper = render()
    await fill(wrapper, raw)
    await wrapper.get('form').trigger('submit')
    expect(importAccount).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="kiro-error"]').text()).not.toContain(raw)
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
  })
  it('clears immediately on submit and shows only safe readiness, never response metadata', async () => {
    const wrapper = render()
    let resolve!: (result: unknown) => void
    importAccount.mockImplementation(() => new Promise(r => { resolve = r }))
    await fill(wrapper, '{"refreshToken":"synthetic-secret","region":"untrusted-json-region","authMethod":"social"}')
    await wrapper.get('[data-testid="kiro-region"]').setValue('us-east-1')
    await wrapper.get('[data-testid="kiro-mode"]').setValue('idc')
    await wrapper.get('form').trigger('submit')
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    expect(importAccount).toHaveBeenCalledWith(expect.objectContaining({ region: 'us-east-1', auth_mode: 'idc' }), 'synthetic-session', expect.any(AbortSignal))
    resolve({ readiness: 'refresh_required', credential_ref: 'must-not-display', accessToken: 'synthetic-secret', metadata: '<script>secret</script>' })
    await flushPromises()
    expect(wrapper.get('[data-testid="kiro-result"]').text()).toContain('Credential refresh required')
    expect(wrapper.text()).toContain('Usage unknown')
    expect(wrapper.text()).not.toContain('must-not-display')
    expect(wrapper.text()).not.toContain('synthetic-secret')
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.emitted('imported')).toHaveLength(1)
  })
  it('never echoes a secret-bearing server failure or logs/persists credentials', async () => {
    const localWrite = vi.spyOn(localStorage, 'setItem')
    const sessionWrite = vi.spyOn(sessionStorage, 'setItem')
    const log = vi.spyOn(console, 'error')
    importAccount.mockRejectedValue({ message: 'synthetic-secret', config: { data: 'synthetic-secret' } })
    const wrapper = render()
    await fill(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-testid="kiro-error"]').text()).not.toContain('synthetic-secret')
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    expect(localWrite).not.toHaveBeenCalled()
    expect(sessionWrite).not.toHaveBeenCalled()
    expect(log).not.toHaveBeenCalled()
  })
  it.each(['ready', 'settlement_required', 'blocked', 'unknown'])('honestly labels readiness %s', async readiness => {
    importAccount.mockResolvedValue({ readiness })
    const wrapper = render(); await fill(wrapper); await wrapper.get('form').trigger('submit'); await flushPromises()
    const text = wrapper.get('[data-testid="kiro-result"]').text()
    expect(text).toContain('Usage unknown')
    expect(text).toContain(readiness === 'ready' ? 'not guaranteed' : readiness === 'unknown' ? 'state unknown' : 'Blocked')
  })
  it('clears secrets even when trusted form configuration is missing', async () => {
    const wrapper = render(); await fill(wrapper)
    await wrapper.get('[data-testid="kiro-user-agent"]').setValue('')
    await wrapper.get('form').trigger('submit')
    expect(importAccount).not.toHaveBeenCalled()
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
  })
  it('clears detached DOM on unmount and aborts pending import', async () => {
    const wrapper = render(); await fill(wrapper)
    const textarea = wrapper.get('textarea').element as HTMLTextAreaElement
    wrapper.unmount()
    expect(textarea.value).toBe('')
    const pending = render(); await fill(pending)
    importAccount.mockImplementation(() => new Promise(() => {}))
    await pending.get('form').trigger('submit')
    const signal = importAccount.mock.calls[0][2] as AbortSignal
    pending.unmount()
    expect(signal.aborted).toBe(true)
  })
  it('does not display late success after closing a pending import', async () => {
    let resolve!: (result: unknown) => void
    importAccount.mockImplementation(() => new Promise(r => { resolve = r }))
    const wrapper = render(); await fill(wrapper); await wrapper.get('form').trigger('submit')
    await wrapper.setProps({ show: false })
    resolve({ readiness: 'ready' }); await flushPromises()
    expect(wrapper.emitted('imported')).toBeUndefined()
    expect(wrapper.find('[data-testid="kiro-result"]').exists()).toBe(false)
  })
  it('clears on external close and explicit clear', async () => {
    const wrapper = render(); await fill(wrapper)
    await wrapper.get('button[type="button"]').trigger('click')
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    await fill(wrapper); await wrapper.setProps({ show: false })
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
  })
})
