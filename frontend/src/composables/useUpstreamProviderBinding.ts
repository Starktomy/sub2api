import { computed, ref, watch, type Ref } from 'vue'
import { getUpstreamProviders, type UpstreamProviderDescriptor } from '@/api/admin/upstreamProviders'

export const isNonOAuthUpstreamType = (type: string): boolean =>
  ['apikey', 'upstream', 'bedrock', 'service_account'].includes(type)

export function providerMatches(provider: UpstreamProviderDescriptor, platform: string, type: string): boolean {
  return provider.platform === platform && provider.account_types.includes(type)
}

export function validProviderCatalog(value: unknown): value is UpstreamProviderDescriptor[] {
  if (!Array.isArray(value)) return false
  const ids = new Set<string>()
  return value.every(p => {
    if (!p || typeof p.id !== 'string' || !p.id || ids.has(p.id) ||
      typeof p.platform !== 'string' || !p.platform ||
      !Array.isArray(p.account_types) || !p.account_types.length ||
      !p.account_types.every((t: unknown) => typeof t === 'string' && isNonOAuthUpstreamType(t)) ||
      !Array.isArray(p.protocols) || !p.protocols.every((v: unknown) => typeof v === 'string') ||
      !p.capabilities || typeof p.capabilities !== 'object' ||
      !['responses', 'chat_completions', 'streaming', 'model_discovery'].every(key =>
        ['unknown', 'supported', 'unsupported'].includes(p.capabilities[key]))) return false
    ids.add(p.id)
    return true
  })
}

/** Binding only touches extra; existing credentials and provider profiles remain owned by the forms. */
export function useUpstreamProviderBinding(
  show: Ref<boolean>, platform: Ref<string>, type: Ref<string>,
  originalExtra?: Ref<Record<string, unknown> | undefined>, identity?: Ref<unknown>
) {
  const selected = ref<unknown>('')
  const providers = ref<UpstreamProviderDescriptor[]>([])
  const loading = ref(false)
  const failed = ref(false)
  const active = computed(() => isNonOAuthUpstreamType(type.value))
  const options = computed(() => providers.value.filter(p => providerMatches(p, platform.value, type.value)))
  const unknownSelection = computed(() => selected.value !== '' &&
    !options.value.some(p => p.id === selected.value))
  let generation = 0

  watch([show, platform, type, () => identity?.value], async () => {
    const request = ++generation
    selected.value = originalExtra?.value && Object.prototype.hasOwnProperty.call(originalExtra.value, 'upstream_provider')
      ? (originalExtra.value.upstream_provider === '' ? null : originalExtra.value.upstream_provider) : ''
    providers.value = []
    failed.value = false
    loading.value = false
    if (!show.value || !active.value) return
    loading.value = true
    try {
      const catalog = await getUpstreamProviders()
      if (request !== generation) return
      if (!validProviderCatalog(catalog)) throw new Error('Invalid upstream metadata')
      providers.value = catalog
      // New accounts bind explicitly when the module has an unambiguous match.
      // Legacy edits deliberately stay unbound.
      if (!originalExtra && options.value.length === 1) selected.value = options.value[0]!.id
    } catch {
      if (request === generation) failed.value = true
    } finally {
      if (request === generation) loading.value = false
    }
  }, { immediate: true })

  const invalid = computed(() => active.value && selected.value !== '' &&
    (loading.value || failed.value || unknownSelection.value))

  function apply(extra: Record<string, unknown> | undefined, targetPlatform: string, targetType: string) {
    if (!isNonOAuthUpstreamType(targetType)) return extra
    if (selected.value !== '' && (loading.value || failed.value || !providers.value.some(p =>
      p.id === selected.value && providerMatches(p, targetPlatform, targetType)))) {
      throw new Error('admin.accounts.upstreamProvider.invalid')
    }
    const result = { ...(extra || {}) }
    if (selected.value === '') delete result.upstream_provider
    else result.upstream_provider = selected.value
    return result
  }

  return { selected, options, active, loading, failed, unknownSelection, invalid, apply }
}
