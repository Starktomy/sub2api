import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamProviderSelect from '../UpstreamProviderSelect.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('upstream provider conditions', () => {
  it('renders authoritative limitations as escaped text for the selected module only', async () => {
    const wrapper = mount(UpstreamProviderSelect, {
      props: {
        id: 'test-provider', modelValue: 'module-a', loading: false, failed: false, unknownSelection: false,
        options: [
          { id: 'module-a', platform: 'test', account_types: ['apikey'], protocols: [], capabilities: {}, conditions: ['Stateless only', '<script>not markup</script>'] },
          { id: 'module-b', platform: 'test', account_types: ['apikey'], protocols: [], capabilities: {}, conditions: ['Public preview'] }
        ]
      }
    })
    expect(wrapper.get('[data-testid="upstream-provider-conditions"]').text()).toContain('Stateless only')
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Public preview')
    await wrapper.setProps({ modelValue: 'module-b' })
    expect(wrapper.get('[data-testid="upstream-provider-conditions"]').text()).toBe('Public preview')
    await wrapper.setProps({ modelValue: '' })
    expect(wrapper.find('[data-testid="upstream-provider-conditions"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
