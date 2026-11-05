import { apiClient } from '../client'

/** Credential-free inventory projection with versioned supply control. */
export interface AdminSupplierResource {
  supplier_name: string
  supplier_user_id: number
  resource: {
    id: string
    display_name: string
    status: string
    version: number
    supply_enabled: boolean
    admin_disabled: boolean
    authorization_version: number
    authorization_expires_at: string | null
    availability_reason?: string | null
    authorized_at?: string | null
    account_id?: number | null
    schedulable: boolean
    probe_state: string
    plan_type: string | null
    masked_email: string | null
    token_expires_at: string | null
    last_probe_at: string | null
    usage?: {
      fetched_at: number
      rate_limit?: {
        primary_window?: { used_percent: number | null; reset_at: number | null } | null
        secondary_window?: { used_percent: number | null; reset_at: number | null } | null
      } | null
    } | null
  }
}

export interface SupplierSettings {
  version: number
  auto_admission: boolean
  group_ids: number[]
  fee_mode: 'standard_cost' | 'customer_charge'
  fee_ratio_bps: number
}

export async function getSupplierSettings(): Promise<SupplierSettings> {
  const { data } = await apiClient.get<SupplierSettings>('/admin/supplier/settings')
  return data
}

export async function saveSupplierSettings(settings: SupplierSettings): Promise<void> {
  await apiClient.put('/admin/supplier/settings', settings)
}

export async function setSupplierSupplyControl(id: string, version: number, disabled: boolean): Promise<AdminSupplierResource['resource']> {
  const { data } = await apiClient.put<AdminSupplierResource['resource']>(`/admin/supplier/resources/${encodeURIComponent(id)}/supply-control`, { version, disabled })
  return data
}

export async function listSupplierResources(): Promise<AdminSupplierResource[]> {
  const { data } = await apiClient.get<{ items: AdminSupplierResource[] | null }>('/admin/supplier/resources')
  return data.items ?? []
}
