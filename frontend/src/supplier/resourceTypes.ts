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
  provider: 'openai'
  product: 'codex'
  schedulable: false
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
export function validSessionJSON(content: string): boolean {
  if (!content.trim() || new TextEncoder().encode(content).length > 1024 * 1024) return false
  try {
    const value: unknown = JSON.parse(content)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return false
    // Supplier import never accepts password, MFA or browser-cookie material.
    const forbidden = /^(password|passwd|cookie|cookies|session_token|sessiontoken|session-token|session_token_raw|totp|totp_code|mfa|mfa_code|two_factor|two_factor_code|2fa)$/i
    function safe(node: unknown): boolean {
      if (!node || typeof node !== 'object') return true
      return Object.entries(node).every(([key, value]) => !forbidden.test(key) && safe(value))
    }
    return safe(value)
  } catch { return false }
}
