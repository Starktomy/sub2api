import { afterEach, describe, expect, it } from 'vitest'
import { computed } from 'vue'
import { CONCRETE_PLATFORM_OPTIONS, GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import {
  BUILTIN_PLATFORM_CATALOG,
  compositePrecedencePlatformIds,
  listPlatformIds,
  platformDisplayName,
  resetPlatformCatalog,
  setPlatformCatalog,
  supportsUpstreamModelSync
} from '@/constants/platformCatalog'
import { platformLabel } from '@/utils/platformColors'
import { normalizePlatformQuotasMap, sanitizePlatformQuotasMap } from '@/api/admin/settings'
import { platformQuotaPlatforms } from '@/api/admin/users'

// Expected public catalog order, including centrally registered compatible providers.
const concretePlatforms = [
  'anthropic',
  'openai',
  'gemini',
  'antigravity',
  'grok',
  'kimi',
  'zhipu',
  'deepseek',
  'minimax',
  'opencode_go',
  'typesafe',
  'command_code',
  'cline',
  'openrouter', 'siliconflow', 'mistral', 'bailian', 'tokenhub', 'qianfan_v2'
]

describe('platform option catalogs', () => {
  it('exposes every concrete account platform', () => {
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(concretePlatforms)
  })

  it('adds composite for group-backed filters', () => {
    expect(GROUP_PLATFORM_OPTIONS.map((option) => option.value)).toEqual([
      ...concretePlatforms,
      'composite'
    ])
  })
})

describe('platform catalog with a newly registered platform', () => {
  const withNewProvider = {
    platforms: [
      ...BUILTIN_PLATFORM_CATALOG.platforms,
      { id: 'acme_router', display_name: 'Acme Router', gateway: 'openai', cn_provider: false }
    ],
    composite_precedence: [...BUILTIN_PLATFORM_CATALOG.composite_precedence, 'acme_router']
  }

  afterEach(() => {
    resetPlatformCatalog()
  })

  it('keeps the built-in catalog equal to the legacy literals', () => {
    expect(listPlatformIds()).toEqual(concretePlatforms)
    expect(compositePrecedencePlatformIds()).toEqual([
      'anthropic', 'gemini', 'openai', 'antigravity', 'grok',
      'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'typesafe', 'command_code', 'cline',
      'openrouter', 'siliconflow', 'mistral', 'bailian', 'tokenhub', 'qianfan_v2'
    ])
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.label)).toEqual([
      'Anthropic', 'OpenAI', 'Gemini', 'Antigravity', 'Grok',
      'Kimi', 'Zhipu GLM', 'DeepSeek', 'MiniMax', 'OpenCode', 'TypeSafe / Jev', 'Command Code', 'Cline',
      'OpenRouter', 'SiliconFlow', 'Mistral', 'Bailian (DashScope compatible)', 'Tencent TokenHub', 'Qianfan V2'
    ])
    expect(platformLabel('zhipu')).toBe('Zhipu GLM')
    expect(platformLabel('composite')).toBe('Composite')
    expect(platformLabel('')).toBe('API')
  })

  it('adds newly registered platforms to options, labels and quota lists reactively', () => {
    const optionValues = computed(() => CONCRETE_PLATFORM_OPTIONS.map((option) => option.value))
    expect(optionValues.value).not.toContain('acme_router')

    setPlatformCatalog(withNewProvider)

    expect(optionValues.value).toEqual([...concretePlatforms, 'acme_router'])
    expect(GROUP_PLATFORM_OPTIONS.map((option) => option.value)).toEqual([
      ...concretePlatforms,
      'acme_router',
      'composite'
    ])
    expect(platformLabel('acme_router')).toBe('Acme Router')
    expect(platformDisplayName('acme_router')).toBe('Acme Router')
    expect(platformLabel('unregistered')).toBe('unregistered')
    expect(platformQuotaPlatforms()).toContain('acme_router')
    expect(Object.keys(normalizePlatformQuotasMap())).toEqual([...concretePlatforms, 'acme_router'])
    expect(sanitizePlatformQuotasMap({ acme_router: { daily: 5, weekly: -1, monthly: null } }).acme_router).toEqual({
      daily: 5,
      weekly: null,
      monthly: null
    })
  })

  it('restores the built-in catalog on reset', () => {
    setPlatformCatalog(withNewProvider)
    resetPlatformCatalog()
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(concretePlatforms)
  })
})


describe('native Messages endpoint boundaries', () => {
  it.each([
    ['openrouter', 'https://openrouter.ai/api/v1'],
    ['siliconflow', 'https://api.siliconflow.cn/v1'],
    ['bailian', 'https://dashscope.aliyuncs.com/apps/anthropic'],
    ['tokenhub', 'https://tokenhub.tencentmaas.com/v1'],
    ['mistral', undefined],
    ['qianfan_v2', undefined]
  ])('uses only the declared native Messages endpoint for %s', (platform, endpoint) => {
    const profile = BUILTIN_PLATFORM_CATALOG.platforms.find(item => item.id === platform)!.multi_protocol!
    const defaults = profile.modes.find(mode => mode.mode === profile.default_mode)!.base_urls
    expect(defaults.anthropic).toBe(endpoint)
  })
})

describe('explicit model discovery policy', () => {
  afterEach(resetPlatformCatalog)

  it.each([
    ['openrouter', true], ['mistral', true], ['qianfan_v2', true],
    ['siliconflow', false], ['bailian', false], ['tokenhub', false]
  ])('uses authoritative model discovery policy for %s', (platform, supported) => {
    expect(supportsUpstreamModelSync(platform as string)).toBe(supported)
  })

  it('does not infer model discovery from compatible profile presence', () => {
    setPlatformCatalog({
      ...BUILTIN_PLATFORM_CATALOG,
      platforms: [...BUILTIN_PLATFORM_CATALOG.platforms, {
        id: 'restricted-compatible', display_name: 'Restricted compatible', gateway: 'openai', cn_provider: false,
        multi_protocol: {
          default_mode: 'payg', routing: 'by_inbound', model_discovery: false,
          modes: [{ mode: 'payg', base_urls: { chat_completions: 'https://example.invalid/v1' } }]
        }
      }]
    })
    expect(supportsUpstreamModelSync('restricted-compatible')).toBe(false)
    expect(supportsUpstreamModelSync('openai')).toBe(true)
    expect(supportsUpstreamModelSync('command_code')).toBe(true)
    expect(supportsUpstreamModelSync('unknown')).toBe(false)
  })
})
