import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ResourceStats from '../ResourceStats.vue'
const { resourceStats } = vi.hoisted(() => ({ resourceStats: vi.fn() }))
vi.mock('../api', () => ({ isolatedSupplierAPI: { resourceStats } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
const stats = { calls: 12, input_tokens: 1234, output_tokens: 567, cache_read_tokens: 890, cache_creation_tokens: 321, fee_amount: '0.12345678', currency: 'USD' }
beforeEach(() => { vi.resetAllMocks(); resourceStats.mockResolvedValue(stats) })

describe('owner resource statistics', () => {
  it('shows loading without invented zero totals, then all recorded values and exact fee', async () => {
    let resolve!: (value: typeof stats) => void
    resourceStats.mockReturnValue(new Promise<typeof stats>(done => { resolve = done }))
    const wrapper = mount(ResourceStats, { props: { resourceId: 'r-1' } })
    expect(wrapper.text()).toContain('supplier.statsLoading')
    expect(wrapper.find('dl').exists()).toBe(false)
    resolve(stats)
    await flushPromises()
    expect(resourceStats).toHaveBeenCalledWith('r-1')
    for (const value of Object.values(stats)) expect(wrapper.text()).toContain(String(value))
    expect(wrapper.text()).toContain('supplier.statsNotice')
  })

  it('shows only aggregate fees, never policy ratios or historical monetary breakdowns', async () => {
    resourceStats.mockResolvedValue({ ...stats, fee_mode: 'secret-policy-mode', fee_ratio_bps: 9876, history: [{ fee_amount: '123456789.00' }] })
    const wrapper = mount(ResourceStats, { props: { resourceId: 'r-1' } })
    await flushPromises()
    expect(wrapper.findAll('dd')).toHaveLength(6)
    expect(wrapper.text()).toContain('0.12345678 USD')
    for (const hidden of ['secret-policy-mode', '9876', '123456789.00']) expect(wrapper.text()).not.toContain(hidden)
  })

  it('shows failure instead of zeros and supports retry without leaking server messages', async () => {
    resourceStats.mockRejectedValueOnce(new Error('private-server-message'))
    const wrapper = mount(ResourceStats, { props: { resourceId: 'r-1' } })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('supplier.statsError')
    expect(wrapper.find('dl').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('private-server-message')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('0.12345678 USD')
  })

  it('displays real zero totals only after a successful response', async () => {
    resourceStats.mockResolvedValue({ ...stats, calls: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0, fee_amount: '0.00000000' })
    const wrapper = mount(ResourceStats, { props: { resourceId: 'r-1' } })
    await flushPromises()
    expect(wrapper.findAll('dd').map(node => node.text())).toEqual(['0', '0', '0', '0', '0', '0.00000000 USD'])
  })

  it('ignores a late response for a previous resource', async () => {
    let resolve!: (value: typeof stats) => void
    resourceStats.mockReturnValueOnce(new Promise<typeof stats>(done => { resolve = done }))
    const wrapper = mount(ResourceStats, { props: { resourceId: 'old' } })
    await wrapper.setProps({ resourceId: 'new' })
    await flushPromises()
    resolve({ ...stats, fee_amount: '999' })
    await flushPromises()
    expect(wrapper.text()).toContain('0.12345678 USD')
    expect(wrapper.text()).not.toContain('999')
  })
})
