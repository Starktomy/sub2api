import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import KiroManagementModal from '../KiroManagementModal.vue'
import { sanitizeKiroRuntime } from '@/api/admin/kiro'
const { auth, request } = vi.hoisted(() => ({ auth: { isAdmin: true, token: 'synthetic-session' }, request: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key.split('.').pop() }) }))
vi.mock('@/api/admin/kiro', async original => ({ ...await original<typeof import('@/api/admin/kiro')>(), requestKiroRuntime: request }))
const state = (refresh = 'idle') => sanitizeKiroRuntime({ credential_version: 2, region: 'us-west-2', auth_mode: 'social', paused: true, readiness: 'settlement_required', refresh_state: refresh, refresh_owner: refresh === 'idle' ? '' : 'opaque-owner', credential_ref: 'never-display', envelope: 'never-display' })
const mounted: ReturnType<typeof mount>[] = []
function render() {
  const wrapper = mount(KiroManagementModal, { props: { show: true, accountId: 7 }, global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } } })
  mounted.push(wrapper); return wrapper
}
beforeEach(() => { auth.isAdmin = true; request.mockReset(); request.mockResolvedValue(state()) })
afterEach(() => { mounted.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks() })
describe('persistent Kiro administration', () => {
  it('loads persisted state once, never automatically queries upstream and separates blockers', async () => {
    request.mockResolvedValue(state('ambiguous'))
    const w = render(); await flushPromises()
    expect(request).toHaveBeenCalledTimes(1)
    expect(request.mock.calls[0][3]).toBe('runtime')
    expect(w.get('[data-testid="kiro-runtime"]').text()).toContain('paused')
    expect(w.get('[data-testid="kiro-runtime"]').text()).toContain('ambiguous')
    expect(w.get('[data-testid="kiro-runtime"]').text()).toContain('settlement')
    expect(w.get('[data-testid="kiro-query-quota"]').attributes('disabled')).toBeDefined()
    expect(w.get('[data-testid="kiro-configure"]').attributes('disabled')).toBeDefined()
    expect(w.text()).not.toContain('opaque-owner'); expect(w.text()).not.toContain('never-display')
    expect(w.get('[data-testid="kiro-quota-snapshot"]').text()).toContain('unknown')
  })
  it.each(['models', 'quota'])('only explicitly queries %s on click', async kind => {
    const w = render(); await flushPromises()
    await w.get(`[data-testid="kiro-query-${kind}"]`).trigger('click'); await flushPromises()
    expect(request).toHaveBeenCalledTimes(2); expect(request.mock.calls[1][3]).toBe(kind)
  })
  it('requires explicit replacement confirmation, submits concurrency guards and clears the secret immediately', async () => {
    request.mockResolvedValue(state('pending'))
    const w = render(); await flushPromises()
    await w.get('textarea').setValue('{"refreshToken":"synthetic-only"}')
    expect(w.get('[data-testid="kiro-reimport"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="kiro-confirm"]').setValue(true)
    let submitted: unknown
    request.mockImplementation((_id, _token, _signal, _action, update) => { submitted = structuredClone(update); return new Promise(() => {}) })
    await w.get('[data-testid="kiro-reimport"]').trigger('submit')
    expect((w.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    expect(submitted).toEqual({ operation: 'reimport', expected_version: 2, expected_refresh_owner: 'opaque-owner', credential: { refreshToken: 'synthetic-only' } })
    expect(request.mock.calls[1][4].credential).toBeUndefined()
    const signal = request.mock.calls[1][2] as AbortSignal
    w.unmount(); expect(signal.aborted).toBe(true)
  })
  it('clears malformed secrets, then sanitizes failure and blocks replay until explicit reload', async () => {
    const w = render(); await flushPromises()
    await w.get('textarea').setValue('malformed secret'); await w.get('[data-testid="kiro-confirm"]').setValue(true)
    await w.get('[data-testid="kiro-reimport"]').trigger('submit')
    expect(request).toHaveBeenCalledTimes(1); expect((w.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    request.mockRejectedValue(new Error('sensitive raw response'))
    await w.get('[data-testid="kiro-pause"]').trigger('click'); await flushPromises()
    expect(w.text()).not.toContain('sensitive raw response'); expect(w.get('[role="alert"]').text()).toBe('failed')
    expect(w.get('[data-testid="kiro-pause"]').attributes('disabled')).toBeDefined()
    expect(request).toHaveBeenCalledTimes(2)
    request.mockResolvedValue(state()); await w.get('[data-testid="kiro-reload"]').trigger('click'); await flushPromises()
    expect(w.get('[data-testid="kiro-pause"]').attributes('disabled')).toBeUndefined()
  })
  it('clears detached textarea on unmount and ignores late results after closing', async () => {
    const w = render(); await flushPromises(); await w.get('textarea').setValue('synthetic')
    const textarea = w.get('textarea').element as HTMLTextAreaElement
    w.unmount(); expect(textarea.value).toBe('')
    let resolve!: (value: unknown) => void
    request.mockImplementation(() => new Promise(r => { resolve = r }))
    const next = render(); await next.setProps({ show: false }); resolve(state()); await flushPromises()
    expect(next.find('[data-testid="kiro-runtime"]').exists()).toBe(false)
  })
  it('renders missing and unexpected states as unknown, not zero or ready', async () => {
    request.mockResolvedValue(sanitizeKiroRuntime({ credential_version: 1, refresh_state: 'future-state', readiness: 'future-state' }))
    const w = render(); await flushPromises()
    expect(w.get('[data-testid="kiro-runtime"]').text()).toContain('unknown')
    expect(w.get('[data-testid="kiro-reimport"]').attributes('disabled')).toBeDefined()
    expect(w.get('[data-testid="kiro-quota-snapshot"]').text()).not.toContain('0')
  })
  it('shows only proven discovered models and keeps listing separate from entitlement', async () => {
    request.mockResolvedValue(sanitizeKiroRuntime({ credential_version: 2, refresh_state: 'idle', models: { state: 'known', source: 'https://management.us-west-2.kiro.dev/', data: { models: [{ modelId: 'sample-model', modelName: 'Sample Model', clientSecret: 'hidden' }] } } }))
    const w = render(); await flushPromises()
    expect(w.get('[data-testid="kiro-models-snapshot"]').text()).toContain('Sample Model (sample-model)')
    expect(w.get('[data-testid="kiro-models-snapshot"]').text()).toContain('upstream')
    expect(w.text()).not.toContain('hidden')
    expect(w.text()).toContain('queryNotice')
  })
  it('saves only operator-supplied limited configuration with the displayed version', async () => {
    const w = render(); await flushPromises()
    await w.get('input[maxlength="512"]').setValue('Legitimate operator client')
    await w.get('[data-testid="kiro-configure"]').trigger('submit'); await flushPromises()
    expect(request.mock.calls[1][4]).toEqual({ operation: 'configure', expected_version: 2, user_agent: 'Legitimate operator client' })
    expect(w.emitted('updated')).toHaveLength(1)
  })
  it('clears secrets on a failed replacement without logging or persisting raw errors', async () => {
    const w = render(); await flushPromises()
    const local = vi.spyOn(localStorage, 'setItem'), session = vi.spyOn(sessionStorage, 'setItem'), log = vi.spyOn(console, 'error')
    await w.get('textarea').setValue('{"refreshToken":"synthetic-only"}')
    await w.get('[data-testid="kiro-confirm"]').setValue(true)
    request.mockRejectedValue({ message: 'synthetic-only', config: { data: 'synthetic-only' } })
    await w.get('[data-testid="kiro-reimport"]').trigger('submit'); await flushPromises()
    expect((w.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    expect(w.text()).not.toContain('synthetic-only'); expect(local).not.toHaveBeenCalled(); expect(session).not.toHaveBeenCalled(); expect(log).not.toHaveBeenCalled()
    expect(request).toHaveBeenCalledTimes(2)
  })
  it('does not request or render management for non-admins', async () => {
    auth.isAdmin = false; const w = render(); await flushPromises()
    expect(request).not.toHaveBeenCalled(); expect(w.text()).toBe('')
  })
})
