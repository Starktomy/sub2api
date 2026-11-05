/** Owner-scoped totals from recorded usage; fee strings retain server precision. */
export interface SupplierResourceStats {
  calls: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  fee_amount: string
  currency: string
}

export interface SupplierResourceWindow {
  used_percent: number | null
  limit_window_seconds: number | null
  reset_after_seconds: number | null
  reset_at: number | null
}

/** Redacted server projection only. No provider credentials belong in this DTO. */
export interface SupplierResource {
  id: string
  display_name: string
  status: 'paused' | 'disconnected'
  version: number
  supply_enabled: boolean
  admin_disabled: boolean
  authorization_version: number
  authorization_expires_at: string | null
  availability_reason?: string | null
  authorized_at?: string | null
  provider: 'openai'
  product: 'codex'
  schedulable: boolean
  masked_email: string | null
  plan_type: string | null
  token_expires_at: string | null
  last_usage_at: string | null
  created_at: string
  updated_at: string
  auto_sync_enabled: boolean
  probe_state: 'pending' | 'running' | 'fresh' | 'retrying' | 'paused' | 'disconnected' | 'reauthorization_required'
  last_probe_at: string | null
  next_probe_at: string | null
  probe_error_code: string | null
  usage_state: 'unknown' | 'fresh' | 'stale'
  usage: { plan_type?: string | null; fetched_at: number; rate_limit?: {
    allowed?: boolean; limit_reached?: boolean
    primary_window?: SupplierResourceWindow | null
    secondary_window?: SupplierResourceWindow | null
  } | null } | null
}
export interface SupplierResourceList { items: SupplierResource[]; vault_configured: boolean }

/** Validate JSON locally without interpreting tokens or retaining a second credential copy. */
export function extractSessionJSON(content: string): string | null {
  if (!content.trim() || new TextEncoder().encode(content).length > 1024 * 1024) return null
  try {
    const value: unknown = JSON.parse(content)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null
    const raw = value as Record<string, unknown>
    if (typeof raw.accessToken === 'string' && raw.account && typeof raw.account === 'object' && !Array.isArray(raw.account) && raw.user && typeof raw.user === 'object' && !Array.isArray(raw.user)) {
      delete raw.sessionToken
      const user = raw.user as Record<string, unknown>
      if (typeof user.mfa === 'boolean') delete user.mfa
    }
    // Never transmit browser session tokens or MFA secrets.
    const forbidden = /^(password|passwd|cookie|cookies|session_token|sessiontoken|session-token|session_token_raw|totp|totp_code|mfa|mfa_code|two_factor|two_factor_code|2fa)$/i
    const safe = (node: unknown): boolean => {
      if (!node || typeof node !== 'object') return true
      return Object.entries(node).every(([key, value]) => !forbidden.test(key) && safe(value))
    }
    if (!safe(value)) return null
    const tokens = raw.tokens && typeof raw.tokens === 'object' ? raw.tokens as Record<string, unknown> : {}
    const token = raw.accessToken ?? raw.access_token ?? tokens.accessToken ?? tokens.access_token
    if (typeof token !== 'string' || !token.trim()) return null
    const result: Record<string, unknown> = { accessToken: token.trim() }
    const account = raw.account as Record<string, unknown> | undefined
    const user = raw.user as Record<string, unknown> | undefined
    const accountID = raw.chatgpt_account_id ?? raw.account_id ?? account?.id ?? account?.account_id
    if (typeof accountID === 'string') result.account_id = accountID
    if (typeof raw.email === 'string' && typeof user?.email === 'string'
      && raw.email.trim().toLowerCase() !== user.email.trim().toLowerCase()) return null
    const email = raw.email ?? user?.email
    if (typeof email === 'string') result.email = email
    return JSON.stringify(result)
  } catch { return null }
}

export function validSessionJSON(content: string): boolean {
  return extractSessionJSON(content) !== null
}
