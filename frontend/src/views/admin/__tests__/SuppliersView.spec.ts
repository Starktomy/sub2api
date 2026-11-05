import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SuppliersView from '../SuppliersView.vue'

const { list, getSettings, saveSettings, getGroups, setSupplyControl } = vi.hoisted(() => ({
  list: vi.fn(), getSettings: vi.fn(), saveSettings: vi.fn(), getGroups: vi.fn(), setSupplyControl: vi.fn()
}))
vi.mock('@/api/admin/suppliers', () => ({ listSupplierResources: list, getSupplierSettings: getSettings, saveSupplierSettings: saveSettings, setSupplierSupplyControl: setSupplyControl }))
vi.mock('@/api/admin/groups', () => ({ getAll: getGroups, default: { getAll: getGroups } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } })
}))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
enableAutoUnmount(afterEach)

function render() {
  return mount(SuppliersView, { global: { stubs: { AppLayout: { template: '<main><slot /></main>' } } } })
}

beforeEach(() => {
  vi.resetAllMocks()
  list.mockResolvedValue([])
  getSettings.mockResolvedValue({ version: 4, auto_admission: false, group_ids: [7], fee_mode: 'standard_cost', fee_ratio_bps: 1250 })
  getGroups.mockResolvedValue([{ id: 7, name: 'OpenAI group', status: 'active', platform: 'openai' }, { id: 8, name: 'Other group', status: 'active', platform: 'claude' }])
  saveSettings.mockResolvedValue(undefined)
})

describe('supplier management', () => {
  it('shows empty inventory honestly and saves policy with admission disabled', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.suppliers.inventoryNotice')
    expect(wrapper.text()).toContain('admin.suppliers.empty')
    expect(wrapper.text()).toContain('OpenAI group')
    expect(wrapper.text()).not.toContain('Other group')
    await wrapper.get('input[type="number"]').setValue('35.25')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith({ version: 4, auto_admission: false, group_ids: [7], fee_mode: 'standard_cost', fee_ratio_bps: 3525 })
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(true)
  })

  it('enables admission only with target groups and reloads the version after saving', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('input[type="checkbox"]').setValue(true)
    getSettings.mockResolvedValue({ version: 5, auto_admission: true, group_ids: [7], fee_mode: 'standard_cost', fee_ratio_bps: 1250 })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveSettings).toHaveBeenLastCalledWith(expect.objectContaining({ version: 4, auto_admission: true }))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveSettings).toHaveBeenLastCalledWith(expect.objectContaining({ version: 5 }))
    await wrapper.get('select[multiple]').setValue([])
    await wrapper.get('form').trigger('submit')
    expect(saveSettings).toHaveBeenCalledTimes(2)
  })

  it('reports policy conflict without retrying or discarding edits', async () => {
    saveSettings.mockRejectedValue({ response: { status: 409 } })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('input[type="number"]').setValue('45')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.suppliers.settingsConflict')
    expect(saveSettings).toHaveBeenCalledTimes(1)
    expect((wrapper.get('input[type="number"]').element as HTMLInputElement).value).toBe('45')
  })

  it('renders redacted inventory and missing metadata without fabricated usage', async () => {
    list.mockResolvedValue([{ supplier_name: 'Supplier A', supplier_user_id: 3, resource: { id: 'r-1', display_name: 'Resource A', status: 'paused', schedulable: false, probe_state: 'fresh', plan_type: null, masked_email: 'm***@example.invalid', token_expires_at: null, last_probe_at: null } }])
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('Supplier A')
    expect(wrapper.text()).toContain('Resource A')
    expect(wrapper.text()).toContain('m***@example.invalid')
    expect(wrapper.text()).toContain('admin.suppliers.unknown')
    expect(wrapper.get('[data-testid="admission"]').text()).toContain('admin.suppliers.notSchedulable')
    expect(wrapper.get('[data-testid="admission"]').text()).toContain('admin.suppliers.noAccount')
  })

  it('uses actual schedulable and account fields independently of paused status', async () => {
    const resource = { id: 'r-1', display_name: 'Resource', status: 'paused', probe_state: 'fresh', plan_type: null, masked_email: null, token_expires_at: null, last_probe_at: null }
    list.mockResolvedValue([
      { supplier_name: 'Supplier', supplier_user_id: 3, resource: { ...resource, schedulable: true, account_id: 42 } },
      { supplier_name: 'Supplier', supplier_user_id: 3, resource: { ...resource, id: 'r-2', schedulable: false, account_id: 43 } },
    ])
    const wrapper = render()
    await flushPromises()
    const cells = wrapper.findAll('[data-testid="admission"]')
    expect(cells[0].text()).toContain('admin.suppliers.schedulable')
    expect(cells[0].text()).toContain('admin.suppliers.accountId: 42')
    expect(cells[1].text()).toContain('admin.suppliers.notSchedulable')
    expect(cells[1].text()).toContain('admin.suppliers.accountId: 43')
  })

  it.each([false, true])('toggles independent administrator prohibition from %s and reloads inventory', async disabled => {
    const resource = { id: 'r-1', display_name: 'Resource', version: 8, status: 'paused', admin_disabled: disabled, supply_enabled: false, probe_state: 'fresh', token_expires_at: null, last_probe_at: null }
    list.mockResolvedValue([{ supplier_name: 'Supplier', supplier_user_id: 3, resource }])
    setSupplyControl.mockResolvedValue({ ...resource, version: 9, admin_disabled: !disabled })
    const wrapper = render()
    await flushPromises()
    const control = wrapper.get('[data-testid="supply-control"]')
    expect(control.text()).toContain(disabled ? 'admin.suppliers.removeProhibition' : 'admin.suppliers.prohibitSupply')
    await control.get('button').trigger('click')
    await flushPromises()
    expect(setSupplyControl).toHaveBeenCalledWith('r-1', 8, !disabled)
    expect(list).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('admin.suppliers.controlSaved')
    expect(saveSettings).not.toHaveBeenCalled()
  })

  it('reports successful control separately from failed inventory reload', async () => {
    const resource = { id: 'r-1', version: 8, admin_disabled: false, status: 'paused' }
    list.mockResolvedValueOnce([{ supplier_user_id: 3, resource }]).mockRejectedValueOnce(new Error('unavailable'))
    setSupplyControl.mockResolvedValue({ ...resource, version: 9, admin_disabled: true })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-testid="supply-control"] button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.suppliers.controlReloadFailed')
    expect(wrapper.text()).toContain('admin.suppliers.loadError')
    expect(setSupplyControl).toHaveBeenCalledTimes(1)
  })

  it('does not send a prohibition change when rollout fields are absent', async () => {
    list.mockResolvedValue([{ supplier_user_id: 3, resource: { id: 'r-1', display_name: 'Resource', status: 'paused' } }])
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-testid="supply-control"] button').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="supply-control"] button').trigger('click')
    expect(setSupplyControl).not.toHaveBeenCalled()
  })

  it('reports supply-control conflicts without silently retrying', async () => {
    list.mockResolvedValue([{ supplier_user_id: 3, resource: { id: 'r-1', version: 8, admin_disabled: false, status: 'paused' } }])
    setSupplyControl.mockRejectedValue({ response: { status: 409 } })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-testid="supply-control"] button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.suppliers.controlConflict')
    expect(setSupplyControl).toHaveBeenCalledTimes(1)
  })

  it('distinguishes API failure from empty inventory and permits retry', async () => {
    list.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.suppliers.loadError')
    expect(wrapper.text()).not.toContain('admin.suppliers.empty')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.suppliers.empty')
  })

  it('blocks policy editing when settings cannot be loaded', async () => {
    getSettings.mockRejectedValue(new Error('unavailable'))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.suppliers.settingsError')
    expect(wrapper.find('form').exists()).toBe(false)
    expect(saveSettings).not.toHaveBeenCalled()
  })
})
