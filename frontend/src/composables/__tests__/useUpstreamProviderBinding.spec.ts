import { describe, it, expect, vi, beforeEach } from 'vitest'
import { effectScope, ref, nextTick } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { useUpstreamProviderBinding, validProviderCatalog } from '../useUpstreamProviderBinding'
import type { UpstreamProviderDescriptor } from '@/api/admin/upstreamProviders'

const { load } = vi.hoisted(() => ({ load: vi.fn() }))
vi.mock('@/api/admin/upstreamProviders', () => ({ getUpstreamProviders: load }))

const modules = [
  ['openai-compatible', 'openai', 'apikey'], ['anthropic-apikey', 'anthropic', 'apikey'],
  ['anthropic-vertex', 'anthropic', 'service_account'], ['gemini-apikey', 'gemini', 'apikey'],
  ['gemini-vertex', 'gemini', 'service_account'], ['bedrock', 'anthropic', 'bedrock'],
  ...['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'command_code', 'cline', 'typesafe'].map(p => [p, p, 'apikey']),
  ['grok-apikey', 'grok', 'apikey'],
  ['antigravity-apikey', 'antigravity', 'apikey'], ['antigravity-upstream', 'antigravity', 'upstream']
]
const catalog: UpstreamProviderDescriptor[] = modules.map(([id, platform, type]) => ({
  id: id!, platform: platform!, account_types: [type!], protocols: [],
  capabilities: { responses: 'unknown', chat_completions: 'unknown', streaming: 'unknown', model_discovery: 'unknown' }
}))

function setup(platform = 'openai', type = 'apikey', extra?: Record<string, unknown>) {
  const scope = effectScope()
  const visible = ref(true)
  const p = ref(platform), t = ref(type)
  const binding = scope.run(() => useUpstreamProviderBinding(visible, p, t, extra ? ref(extra) : undefined))!
  return { binding, scope, p, t, visible }
}

beforeEach(() => { load.mockReset(); load.mockResolvedValue(catalog) })

describe('upstream module binding', () => {
  it.each(modules)('selects matching nonOAuth module %s for %s/%s', async (id, platform, type) => {
    const { binding, scope } = setup(platform, type)
    await flushPromises()
    expect(binding.selected.value).toBe(id)
    expect(binding.apply({ retained: 1 }, platform!, type!)).toEqual({ retained: 1, upstream_provider: id })
    scope.stop()
  })

  it.each(['oauth', 'setup-token', 'sso', 'session'])('never binds or fetches metadata for %s', async type => {
    const { binding, scope } = setup('openai', type)
    await flushPromises()
    expect(load).not.toHaveBeenCalled()
    expect(binding.active.value).toBe(false)
    expect(binding.apply({ retained: 1 }, 'openai', type)).toEqual({ retained: 1 })
    scope.stop()
  })

  it('keeps legacy editing unbound and retains unrelated extra', async () => {
    const { binding, scope } = setup('openai', 'apikey', { retained: 1 })
    await flushPromises()
    expect(binding.selected.value).toBe('')
    expect(binding.apply({ retained: 1 }, 'openai', 'apikey')).toEqual({ retained: 1 })
    scope.stop()
  })

  it.each(['unknown-module', 'gemini-apikey', '', null, 42])('does not silently discard invalid binding %s', async value => {
    const { binding, scope } = setup('openai', 'apikey', { upstream_provider: value })
    await flushPromises()
    expect(binding.unknownSelection.value).toBe(true)
    expect(() => binding.apply({ upstream_provider: value }, 'openai', 'apikey')).toThrow()
    binding.selected.value = '' // explicit legacy choice
    expect(binding.apply({ upstream_provider: value, retained: 1 }, 'openai', 'apikey')).toEqual({ retained: 1 })
    scope.stop()
  })

  it('fails closed when bound metadata is unavailable or mismatched', async () => {
    load.mockRejectedValue(new Error('offline'))
    const { binding, scope } = setup('openai', 'apikey', { upstream_provider: 'openai-compatible' })
    await flushPromises()
    expect(binding.failed.value).toBe(true)
    expect(() => binding.apply({}, 'openai', 'apikey')).toThrow()
    scope.stop()
    expect(validProviderCatalog([{ ...catalog[0], account_types: ['oauth'] }])).toBe(false)
    expect(validProviderCatalog([catalog[0], catalog[0]])).toBe(false)
  })

  it('ignores a stale metadata response after switching to OAuth', async () => {
    let resolve!: (value: UpstreamProviderDescriptor[]) => void
    load.mockReturnValue(new Promise(r => { resolve = r }))
    const { binding, scope, t } = setup()
    t.value = 'oauth'
    await nextTick()
    resolve(catalog)
    await flushPromises()
    expect(binding.selected.value).toBe('')
    expect(binding.options.value).toEqual([])
    scope.stop()
  })
})
