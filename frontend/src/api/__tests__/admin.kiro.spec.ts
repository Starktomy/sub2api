import { afterEach, describe, expect, it, vi } from 'vitest'
import { importKiroAccount, parseKiroImport, type KiroImportRequest } from '../admin/kiro'

const request: KiroImportRequest = { name: 'Synthetic', credential: { refreshToken: 'synthetic-not-real' }, region: 'us-west-2', auth_mode: 'social', user_agent: 'Legitimate client', amz_user_agent: 'Legitimate sdk' }
afterEach(() => vi.unstubAllGlobals())
describe('secret-safe native Kiro request boundary', () => {
  it('posts once to the fixed admin endpoint, without replay, and returns allowlisted safe fields', async () => {
    const fetcher = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ code: 0, data: { account_id: 1, credential_ref: 'never-display', credential_version: 2, readiness: 'settlement_required', secret: 'synthetic-not-real' } }) })
    vi.stubGlobal('fetch', fetcher)
    const signal = new AbortController().signal
    expect(await importKiroAccount(request, 'session', signal)).toEqual({ readiness: 'settlement_required' })
    expect(fetcher).toHaveBeenCalledOnce()
    expect(fetcher).toHaveBeenCalledWith('/api/v1/admin/accounts/kiro/import', expect.objectContaining({ method: 'POST', cache: 'no-store', redirect: 'error', signal, headers: expect.objectContaining({ 'X-Admin-UI-Request': '1' }), body: JSON.stringify(request) }))
  })
  it.each([401, 400, 500])('does not read error bodies or replay HTTP %s', async status => {
    const json = vi.fn().mockResolvedValue({ message: 'synthetic-secret' })
    const fetcher = vi.fn().mockResolvedValue({ ok: false, status, json })
    vi.stubGlobal('fetch', fetcher)
    await expect(importKiroAccount(request, 'session', new AbortController().signal)).rejects.toThrow('Kiro import failed')
    expect(json).not.toHaveBeenCalled()
    expect(fetcher).toHaveBeenCalledOnce()
  })
  it('sanitizes transport failures and application errors', async () => {
    const fetcher = vi.fn().mockRejectedValue(new Error('synthetic-secret'))
    vi.stubGlobal('fetch', fetcher)
    await expect(importKiroAccount(request, 'session', new AbortController().signal)).rejects.toThrow(/^Kiro import failed$/)
    fetcher.mockResolvedValue({ ok: true, json: async () => ({ code: 400, message: 'synthetic-secret' }) })
    await expect(importKiroAccount(request, 'session', new AbortController().signal)).rejects.toThrow(/^Kiro import failed$/)
  })
  it('maps unexpected readiness to unknown and drops all other metadata', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ account_id: 1, credential_version: 1, readiness: '<script>secret</script>', credential: request.credential }) }))
    expect(await importKiroAccount(request, 'session', new AbortController().signal)).toEqual({ readiness: 'unknown' })
  })
  it('does not claim import success for malformed success responses', async () => {
    for (const data of [null, {}, { readiness: 'ready' }, { account_id: -1, credential_version: 1, readiness: 'ready' }]) {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => data }))
      await expect(importKiroAccount(request, 'session', new AbortController().signal)).rejects.toThrow(/^Kiro import failed$/)
    }
  })
  it('rejects oversized JSON, arbitrary routing/proxy fields and unsupported input', () => {
    for (const raw of ['"' + 'x'.repeat(65536) + '"', '{}', '[]', '{"refreshToken":"synthetic","endpoint":"https://untrusted.invalid"}', '{"refreshToken":"synthetic","proxyUrl":"http://untrusted.invalid"}', '{"refreshToken":"synthetic","__proto__":{}}']) {
      expect(() => parseKiroImport(raw)).toThrow(/^Invalid Kiro JSON$/)
    }
    expect(parseKiroImport('{"refreshToken":"synthetic"}')).toEqual({ refreshToken: 'synthetic' })
  })
})

describe('persistent Kiro runtime boundary', () => {
  it('projects unknown/missing data without invented zero values or raw metadata', async () => {
    const { sanitizeKiroRuntime } = await import('../admin/kiro')
    const result = sanitizeKiroRuntime({ credential_version: 1, credential_ref: 'hidden', token: 'hidden', refresh_state: 'future', quota: { state: 'known', source: 'untrusted-secret', data: { usageBreakdownList: [{ currentUsage: null, usageLimit: -1 }, { currentUsageWithPrecision: 1.5, usageLimit: 10 }] } } })
    expect(result.paused).toBeNull(); expect(result.readiness).toBe('unknown'); expect(result.refreshState).toBe('unknown')
    expect(result.quota.quota).toEqual([]); expect(result.quota.state).toBe('unknown'); expect(result.quota.source).toBe('unknown')
    expect(JSON.stringify(result)).not.toContain('hidden'); expect(JSON.stringify(result)).not.toContain('untrusted-secret')
  })
  it('does not read bodies, leak failures, or replay a secret write after unauthorized response', async () => {
    const { requestKiroRuntime } = await import('../admin/kiro')
    const json = vi.fn(); const fetcher = vi.fn().mockResolvedValue({ ok: false, status: 401, json })
    vi.stubGlobal('fetch', fetcher)
    await expect(requestKiroRuntime(7, 'session', new AbortController().signal, 'runtime', { expected_version: 2, operation: 'reimport', credential: { refreshToken: 'synthetic-only' } })).rejects.toThrow(/^Kiro action failed$/)
    expect(fetcher).toHaveBeenCalledOnce(); expect(json).not.toHaveBeenCalled()
    expect(fetcher.mock.calls[0][1]).toMatchObject({ method: 'PATCH', cache: 'no-store', redirect: 'error' })
  })
  it('only accepts known provenance and numerics, stripping unknown snapshot data', async () => {
    const { sanitizeKiroRuntime } = await import('../admin/kiro')
    const result = sanitizeKiroRuntime({ credential_version: 2, models: { state: 'unknown', source: 'unsupported_reference', data: { models: [{ modelId: 'invented' }] } }, quota: { state: 'known', source: 'https://q.us-west-2.amazonaws.com/getUsageLimits', observed_at: '2026-11-04T12:00:00Z', data: { token: 'hidden', usageBreakdownList: [{ currentUsage: 0, usageLimit: 5 }] } } })
    expect(result.models.models).toEqual([]); expect(result.models.source).toBe('unsupported_reference')
    expect(result.quota.source).toBe('upstream'); expect(result.quota.observedAt).toBe('2026-11-04T12:00:00.000Z')
    expect(result.quota.quota).toEqual([{ currentUsage: 0, usageLimit: 5 }])
    const unsupported = sanitizeKiroRuntime({ credential_version: 2, models: { state: 'known', source: 'https://q.us-west-2.amazonaws.com/getUsageLimits', data: { models: [{ modelId: 'fabricated-entitlement' }] } } })
    expect(unsupported.models.state).toBe('unknown'); expect(unsupported.models.models).toEqual([])
  })
})

describe('proven Kiro model discovery', () => {
  it('projects only discovery-endpoint model IDs/names and no credential fields', async () => {
    const { sanitizeKiroRuntime } = await import('../admin/kiro')
    const runtime = sanitizeKiroRuntime({ credential_version: 1, models: { state: 'known', source: 'https://management.us-west-2.kiro.dev/', data: { models: [{ modelId: 'sample-model-v1', modelName: 'Sample Model', token: 'hidden' }, { modelId: '<script>hidden</script>' }, { modelId: 'sample-model-v2', modelName: '<script>hidden</script>' }] } } })
    expect(runtime.models).toMatchObject({ state: 'known', source: 'upstream', models: [{ id: 'sample-model-v1', name: 'Sample Model' }, { id: 'sample-model-v2' }] })
    expect(JSON.stringify(runtime)).not.toContain('hidden')
  })
})
