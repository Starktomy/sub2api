import { apiClient } from './client'

export interface SupplierProfile {
  id: number
  user_id: number
  display_name: string
  status: 'active'
  verified_email: string
  email_verified_at: string
  version: number
  created_at: string
  updated_at: string
}

// Identity, verification target and status are always determined by the server.
export const supplierAPI = {
  async getProfile(): Promise<SupplierProfile> {
    const { data } = await apiClient.get<SupplierProfile>('/supplier/profile')
    return data
  },
  async sendVerifyCode(): Promise<void> {
    await apiClient.post('/supplier/send-verify-code', {})
  },
  async enroll(displayName: string, verifyCode: string): Promise<SupplierProfile> {
    const { data } = await apiClient.post<SupplierProfile>('/supplier/enroll', {
      display_name: displayName, verify_code: verifyCode
    })
    return data
  }
}
