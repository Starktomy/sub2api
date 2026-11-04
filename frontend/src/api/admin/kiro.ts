import { buildApiUrl } from '../url'
import { ADMIN_UI_REQUEST_HEADER } from '../adminUIRequest'

export type KiroRegion = 'us-east-1' | 'us-west-2'
export type KiroAuthMode = 'social' | 'idc'
export type KiroReadiness = 'ready' | 'refresh_required' | 'settlement_required' | 'blocked' | 'unknown'
export interface KiroImportRequest {
  name: string
  credential: Record<string, unknown>
  region: KiroRegion
  auth_mode: KiroAuthMode
  user_agent: string
  amz_user_agent: string
  machine_id?: string
}
export interface KiroImportResult { readiness: KiroReadiness }

/** Secret-bearing one-shot request: no Axios config retention, auth replay, or raw errors. */
export async function importKiroAccount(request: KiroImportRequest, token: string, signal: AbortSignal): Promise<KiroImportResult> {
  try {
    const response = await fetch(buildApiUrl('/admin/accounts/kiro/import'), {
      method: 'POST', credentials: 'include', cache: 'no-store', redirect: 'error', signal,
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, [ADMIN_UI_REQUEST_HEADER]: '1' },
      body: JSON.stringify(request)
    })
    if (!response.ok) throw new Error('Kiro import failed')
    const envelope = await response.json()
    if (envelope?.code !== undefined && envelope.code !== 0) throw new Error('Kiro import failed')
    const data = envelope?.code === 0 ? envelope.data : envelope
    if (!Number.isSafeInteger(data?.account_id) || data.account_id <= 0 || !Number.isSafeInteger(data?.credential_version) || data.credential_version <= 0) throw new Error('Kiro import failed')
    const readiness = data.readiness
    // Never return arbitrary server metadata, credential refs, or error messages to the UI.
    return { readiness: ['ready', 'refresh_required', 'settlement_required', 'blocked'].includes(readiness) ? readiness : 'unknown' }
  } catch {
    throw new Error('Kiro import failed')
  }
}

/** Convenience checks only. The server remains the authority for all import validation. */
export function parseKiroImport(raw: string): Record<string, unknown> {
  if (new TextEncoder().encode(raw).length > 65536) throw new Error('Invalid Kiro JSON')
  let value: unknown
  try { value = JSON.parse(raw) } catch { throw new Error('Invalid Kiro JSON') }
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid Kiro JSON')
  const fields = new Set(['accessToken', 'refreshToken', 'expiresAt', 'authMethod', 'clientId', 'clientSecret', 'profileArn', 'id', 'priority', 'region', 'authRegion', 'apiRegion', 'email', 'subscriptionTitle', 'disabled', 'machineId', 'provider'])
  if (Object.entries(value).some(([key, field]) => !fields.has(key) || field === null || typeof field === 'object') || !['accessToken', 'refreshToken'].some(key => typeof (value as Record<string, unknown>)[key] === 'string' && (value as Record<string, string>)[key].trim())) {
    throw new Error('Invalid Kiro JSON')
  }
  return value as Record<string, unknown>
}

export interface KiroSnapshot {
  state: 'known' | 'unknown'
  source: 'upstream' | 'unsupported_reference' | 'unknown'
  observedAt: string | null
  models: { id: string; name?: string }[]
  quota: { currentUsage?: number; usageLimit?: number }[]
}
export interface KiroRuntime {
  version: number
  region: KiroRegion | 'unknown'
  authMode: KiroAuthMode | 'unknown'
  refreshState: 'idle' | 'pending' | 'ambiguous' | 'unknown'
  // Opaque concurrency guard; never rendered, logged, or persisted in browser storage.
  refreshOwner: string
  paused: boolean | null
  readiness: KiroReadiness
  models: KiroSnapshot
  quota: KiroSnapshot
}
export type KiroRuntimeUpdate = {
  expected_version: number
  operation: 'pause' | 'configure' | 'reimport'
  expected_refresh_owner?: string
  user_agent?: string
  amz_user_agent?: string
  machine_id?: string
  credential?: Record<string, unknown>
}
function snapshot(raw: unknown, kind: 'models' | 'quota'): KiroSnapshot {
  const value = raw && typeof raw === 'object' ? raw as Record<string, unknown> : {}
  const data = value.data && typeof value.data === 'object' ? value.data as Record<string, unknown> : {}
  const supported = kind === 'quota'
    ? value.source === 'https://q.us-east-1.amazonaws.com/getUsageLimits' || value.source === 'https://q.us-west-2.amazonaws.com/getUsageLimits'
    : value.source === 'https://management.us-east-1.kiro.dev/' || value.source === 'https://management.us-west-2.kiro.dev/'
  const known = value.state === 'known' && supported
  const date = typeof value.observed_at === 'string' && /^\d{4}-\d{2}-\d{2}T[\d:.+-]+Z?$/.test(value.observed_at) ? new Date(value.observed_at) : null
  return {
    state: known ? 'known' : 'unknown',
    source: supported ? 'upstream' : value.source === 'unsupported_reference' ? 'unsupported_reference' : 'unknown',
    observedAt: date && Number.isFinite(date.getTime()) ? date.toISOString() : null,
    // Only the proven management endpoint authorizes model discovery; never use a static fallback.
    models: known && kind === 'models' && Array.isArray(data.models) ? data.models.slice(0, 100).flatMap((model: unknown) => {
      if (!model || typeof model !== 'object') return []
      const row = model as Record<string, unknown>
      if (typeof row.modelId !== 'string' || !/^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,159}$/.test(row.modelId)) return []
      return [{ id: row.modelId, ...(typeof row.modelName === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9 ._:/()\-]{0,119}$/.test(row.modelName) ? { name: row.modelName } : {}) }]
    }) : [],
    quota: known && Array.isArray(data.usageBreakdownList) ? data.usageBreakdownList.slice(0, 100).map((item: unknown) => {
      const row = item && typeof item === 'object' ? item as Record<string, unknown> : {}
      const finite = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n) && n >= 0
      const current = finite(row.currentUsageWithPrecision) ? row.currentUsageWithPrecision : row.currentUsage
      const limit = finite(row.usageLimitWithPrecision) ? row.usageLimitWithPrecision : row.usageLimit
      return { ...(finite(current) ? { currentUsage: current } : {}), ...(finite(limit) ? { usageLimit: limit } : {}) }
    }) : []
  }
}
/** Allowlist persisted administrative state. Credentials, refs and envelopes never cross into the view. */
export function sanitizeKiroRuntime(raw: unknown): KiroRuntime {
  if (!raw || typeof raw !== 'object') throw new Error('Kiro action failed')
  const value = raw as Record<string, unknown>
  if (!Number.isSafeInteger(value.credential_version) || (value.credential_version as number) <= 0) throw new Error('Kiro action failed')
  return {
    version: value.credential_version as number,
    region: value.region === 'us-east-1' || value.region === 'us-west-2' ? value.region : 'unknown',
    authMode: value.auth_mode === 'social' || value.auth_mode === 'idc' ? value.auth_mode : 'unknown',
    refreshState: value.refresh_state === 'idle' || value.refresh_state === 'pending' || value.refresh_state === 'ambiguous' ? value.refresh_state : 'unknown',
    refreshOwner: typeof value.refresh_owner === 'string' ? value.refresh_owner : '',
    paused: typeof value.paused === 'boolean' ? value.paused : null,
    readiness: value.readiness === 'ready' || value.readiness === 'blocked' || value.readiness === 'settlement_required' || value.readiness === 'refresh_required' ? value.readiness : 'unknown',
    models: snapshot(value.models, 'models'), quota: snapshot(value.quota, 'quota')
  }
}
/** All management calls are one-shot, especially secret writes: no auth refresh/replay or raw errors. */
export async function requestKiroRuntime(id: number, token: string, signal: AbortSignal, action: 'runtime' | 'models' | 'quota' = 'runtime', update?: KiroRuntimeUpdate): Promise<KiroRuntime> {
  try {
    if (!Number.isSafeInteger(id) || id <= 0) throw new Error('Kiro action failed')
    const response = await fetch(buildApiUrl(`/admin/accounts/${id}/kiro/${action}`), {
      method: update ? 'PATCH' : action === 'runtime' ? 'GET' : 'POST', credentials: 'include', cache: 'no-store', redirect: 'error', signal,
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, [ADMIN_UI_REQUEST_HEADER]: '1' },
      ...(update ? { body: JSON.stringify(update) } : {})
    })
    if (!response.ok) throw new Error('Kiro action failed')
    const envelope = await response.json()
    if (envelope?.code !== undefined && envelope.code !== 0) throw new Error('Kiro action failed')
    return sanitizeKiroRuntime(envelope?.code === 0 ? envelope.data : envelope)
  } catch { throw new Error('Kiro action failed') }
}
