import { apiClient } from '../client'

export type UpstreamSupport = 'unknown' | 'supported' | 'unsupported'
export interface UpstreamProviderDescriptor {
  id: string
  display_name?: string
  authentication?: string[]
  execution_family?: string
  operations?: string[]
  conditions?: string[]
  platform: string
  account_types: string[]
  protocols: string[]
  capabilities: Record<string, UpstreamSupport>
}

/** Credential-free metadata, exposed only by the authenticated admin API. */
export async function getUpstreamProviders(): Promise<UpstreamProviderDescriptor[]> {
  const { data } = await apiClient.get<{ providers: UpstreamProviderDescriptor[] }>('/admin/upstream-providers')
  return data.providers
}
