<template>
  <section class="card min-w-0 space-y-5 p-4 sm:p-6">
    <h2 class="text-xl font-semibold">{{ t('supplier.resourcesTitle') }}</h2>
    <p>{{ t('supplier.resourceBoundary') }}</p>
    <div class="flex flex-wrap gap-3">
      <button class="btn btn-primary" :disabled="busy || !vaultConfigured" @click="openImport">{{ t('supplier.authorizeAndStart') }}</button>
      <button class="btn btn-secondary" :disabled="busy || loading || importOpen" @click="load">{{ t('supplier.reloadResources') }}</button>
    </div>
    <p v-if="!loading && !loadFailed && !vaultConfigured" role="status">{{ t('supplier.importUnconfigured') }}</p>
    <form v-if="importOpen" class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-700" @submit.prevent="importSession">
      <h3 class="font-semibold">{{ t(updatingResource ? 'supplier.updateAuthorization' : 'supplier.importSession') }}</h3>
      <p v-if="updatingResource">{{ t('supplier.updateAuthorizationNotice') }}</p>
      <p>{{ t('supplier.sessionSafety') }}</p>
      <label for="resource-name" class="block">{{ t('supplier.displayName') }}</label>
      <input id="resource-name" v-model="name" class="input" required maxlength="100" autocomplete="off" :disabled="busy" />
      <label for="resource-session" class="block">{{ t('supplier.sessionJSON') }}</label>
      <textarea id="resource-session" v-model="content" class="input min-h-40" required autocomplete="off" autocapitalize="off" spellcheck="false" data-private="true" :disabled="busy" />
      <p id="resource-consent" class="text-sm">{{ t('supplier.supplyConsent') }}</p>
      <label class="flex items-start gap-2">
        <input id="resource-consent-check" v-model="consented" type="checkbox" required aria-describedby="resource-consent" :disabled="busy" />
        <span>{{ t('supplier.confirmSupplyConsent') }}</span>
      </label>
      <div class="flex flex-wrap gap-3">
        <button type="submit" class="btn btn-primary" :disabled="busy || !consented">{{ t(busy ? 'supplier.working' : 'supplier.authorizeAndStart') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="closeImport">{{ t('supplier.cancel') }}</button>
      </div>
    </form>
    <p v-if="error" role="alert" class="text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="success" role="status">{{ success }}</p>
    <p v-if="listRefreshFailed" role="status">{{ t('supplier.syncDisplayStale') }}</p>
    <p v-if="loading" role="status">{{ t('supplier.loadingResources') }}</p>
    <div v-else-if="loadFailed">
      <p>{{ t('supplier.resourcesFailed') }}</p>
      <button class="btn btn-secondary" :disabled="busy" @click="load">{{ t('supplier.retry') }}</button>
    </div>
    <p v-else-if="!resources.length">{{ t('supplier.noResources') }}</p>
    <ul v-else class="space-y-4">
      <li v-for="resource in resources" :key="resource.id" class="min-w-0 break-words space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
        <h3 class="font-semibold">{{ resource.display_name }}</h3>
        <p>{{ resource.masked_email || t('supplier.unknown') }} · {{ t(resource.usage ? 'supplier.observedPlan' : 'supplier.declaredPlan') }}: {{ resource.usage?.plan_type || resource.plan_type || t('supplier.unknown') }}</p>
        <p>{{ supplyState(resource) }} · {{ t(resource.schedulable ? 'supplier.schedulable' : 'supplier.notSchedulable') }}</p>
        <p v-if="resource.admin_disabled" role="status">{{ t('supplier.adminDisabledNotice') }}</p>
        <p v-if="resource.availability_reason" role="status">{{ availabilityReason(resource.availability_reason) }}</p>
        <p>{{ t('supplier.authorizationExpiry') }}: {{ date(resource.authorization_expires_at) }}</p>
        <div class="space-y-2 rounded-lg bg-gray-50 p-3 dark:bg-dark-900">
          <p>{{ t('supplier.autoSync') }}: {{ t(resource.auto_sync_enabled ? 'supplier.syncRequested' : 'supplier.syncDisabled') }}</p>
          <p>{{ t('supplier.syncState') }}: {{ syncState(resource.probe_state) }}</p>
          <p>{{ t('supplier.lastProbe') }}: {{ date(resource.last_probe_at) }}</p>
          <p>{{ t('supplier.nextProbe') }}: {{ date(resource.next_probe_at) }}</p>
          <p v-if="resource.probe_error_code" role="status">{{ probeError(resource.probe_error_code) }}</p>
          <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('supplier.syncWorkerCaveat') }}</p>
        </div>
        <p>{{ t('supplier.updatedAt') }}: {{ date(resource.updated_at) }}</p>
        <p>{{ t('supplier.tokenExpiry') }}: {{ date(resource.token_expires_at) }}</p>
        <p>{{ t('supplier.usageUpdatedAt') }}: {{ date(resource.last_usage_at) }} · {{ t(resource.usage_state === 'fresh' ? 'supplier.snapshot' : resource.usage_state === 'stale' ? 'supplier.stale' : 'supplier.unknown') }}</p>
        <dl class="space-y-2">
          <template v-for="window in ['primary_window', 'secondary_window'] as const" :key="window">
            <dt>{{ t(window === 'primary_window' ? 'supplier.primaryWindow' : 'supplier.secondaryWindow') }}</dt>
            <dd>{{ percent(resource.usage?.rate_limit?.[window]?.used_percent) }} · {{ t('supplier.resetsAt') }}: {{ unixDate(resource.usage?.rate_limit?.[window]?.reset_at) }}</dd>
          </template>
        </dl>
        <div class="flex flex-wrap gap-3">
          <template v-if="resource.status !== 'disconnected'">
            <button class="btn btn-secondary" :disabled="busy || importOpen" @click="action(resource, 'usage')">{{ t('supplier.refreshUsage') }}</button>
            <button class="btn btn-primary" :disabled="busy || importOpen || !canStart(resource)" @click="action(resource, 'start')">{{ t('supplier.startSupply') }}</button>
            <button class="btn btn-secondary" :disabled="busy || importOpen" @click="action(resource, 'pause')">{{ t('supplier.pauseSupply') }}</button>
            <button class="btn btn-secondary" :disabled="busy || importOpen" @click="disconnecting = resource.id">{{ t('supplier.disconnect') }}</button>
          </template>
          <button class="btn btn-secondary" :disabled="busy || !vaultConfigured" @click="updateAuthorization(resource)">{{ t('supplier.updateAuthorization') }}</button>
        </div>
        <div v-if="disconnecting === resource.id" class="space-y-2">
          <p>{{ t('supplier.disconnectConfirm') }}</p>
          <button class="btn btn-danger" :disabled="busy" @click="action(resource, 'disconnect')">{{ t('supplier.confirmDisconnect') }}</button>
          <button class="btn btn-secondary" :disabled="busy" @click="disconnecting = null">{{ t('supplier.cancel') }}</button>
        </div>
        <ResourceStats :resource-id="resource.id" />
      </li>
    </ul>
  </section>
</template>
<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ResourceStats from './ResourceStats.vue'
import { isolatedSupplierAPI, supplierErrorReason } from './api'
import { extractSessionJSON, type SupplierResource } from './resourceTypes'
const { t } = useI18n()
const resources = ref<SupplierResource[]>([])
const vaultConfigured = ref(false)
const loading = ref(true)
const listRefreshFailed = ref(false)
const loadFailed = ref(false)
const busy = ref(false)
const importOpen = ref(false)
const name = ref('')
const content = ref('')
const error = ref('')
const success = ref('')
const disconnecting = ref<string | null>(null)
const consented = ref(false)
const updatingResource = ref<{ id: string; version: number } | null>(null)
function closeImport() { content.value = ''; name.value = ''; consented.value = false; updatingResource.value = null; importOpen.value = false }
function openImport() { closeImport(); disconnecting.value = null; error.value = ''; success.value = ''; importOpen.value = true }
onUnmounted(closeImport)
function syncState(state: string) {
  const states: Record<string, string> = { pending: 'syncPending', running: 'syncRunning', fresh: 'syncFresh', retrying: 'syncRetrying', paused: 'syncPaused', disconnected: 'disconnected', reauthorization_required: 'syncReauthorize' }
  return t(`supplier.${states[state] || 'unknown'}`)
}
function probeError(code: string) {
  const reasons: Record<string, string> = { expired: 'syncExpired', unauthorized: 'syncUnauthorized', forbidden: 'syncForbidden', rate_limited: 'syncRateLimited', upstream_unavailable: 'syncUnavailable', vault_unavailable: 'importUnconfigured' }
  return t(`supplier.${reasons[code] || 'syncFailedSafe'}`)
}
function updateAuthorization(resource: SupplierResource) {
  if (busy.value || !vaultConfigured.value) return
  openImport()
  updatingResource.value = { id: resource.id, version: resource.version }
  name.value = resource.display_name
}
function authorizationValid(resource: SupplierResource) {
  const expiresAt = resource.authorization_expires_at ? Date.parse(resource.authorization_expires_at) : NaN
  return resource.authorization_version > 0 && Number.isFinite(expiresAt) && expiresAt > Date.now()
}
function canStart(resource: SupplierResource) {
  return resource.status !== 'disconnected' && resource.admin_disabled === false && resource.supply_enabled === false
    && authorizationValid(resource) && resource.probe_state !== 'reauthorization_required'
}
function supplyState(resource: SupplierResource) {
  if (resource.status === 'disconnected') return t('supplier.disconnected')
  if (resource.admin_disabled) return t('supplier.adminDisabled')
  if (!authorizationValid(resource)) return t('supplier.authorizationRequired')
  if (resource.availability_reason === 'verification_required') return t('supplier.verificationPending')
  return t(resource.supply_enabled ? 'supplier.supplyEnabled' : 'supplier.supplyPaused')
}
function availabilityReason(reason: string) {
  const reasons: Record<string, string> = {
    admin_disabled: 'adminDisabledNotice', authorization_required: 'authorizationRequired',
    authorization_expired: 'authorizationRequired', supply_paused: 'supplyPaused',
    disconnected: 'disconnected', reauthorization_required: 'syncReauthorize',
    verification_required: 'verificationRequired', quota_unknown: 'quotaUnknown',
    quota_exhausted: 'quotaExhausted', quota_stale: 'quotaStale', unavailable: 'availabilityPending'
  }
  return t(`supplier.${reasons[reason] || 'availabilityPending'}`)
}
function date(value?: string | null) {
  if (!value || !Number.isFinite(Date.parse(value))) return t('supplier.unknown')
  return new Date(value).toLocaleString()
}
function percent(value?: number | null) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 100 ? `${value}%` : t('supplier.unknown')
}
function unixDate(value?: number | null) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0 || value > 8640000000000) return t('supplier.unknown')
  return date(new Date(value * 1000).toISOString())
}

function message(err: unknown) {
  const reason = supplierErrorReason(err)
  if (reason === 'SUPPLIER_SUPPLY_UNAVAILABLE') return t('supplier.supplyUnavailable')
  if (reason === 'SUPPLIER_CONSENT_REQUIRED') return t('supplier.consentRequired')
  if (reason === 'SUPPLIER_AUTHORIZATION_EXPIRY_INVALID') return t('supplier.authorizationExpiryInvalid')
  if (reason === 'SUPPLIER_VAULT_NOT_CONFIGURED') return t('supplier.importUnconfigured')
  if (reason === 'SUPPLIER_RESOURCE_VERSION_CONFLICT') return t('supplier.resourceConflict')
  if (reason === 'SUPPLIER_RESOURCE_REIMPORT_REQUIRED') return t('supplier.reimportRequired')
  if (reason === 'SUPPLIER_RESOURCE_CONFLICT') return t('supplier.resourceDuplicate')
  return t('supplier.resourceActionFailed')
}
async function load() {
  loading.value = true
  loadFailed.value = false
  listRefreshFailed.value = false
  try {
    const data = await isolatedSupplierAPI.resources()
    resources.value = data.items || []
    vaultConfigured.value = data.vault_configured
  } catch { loadFailed.value = true; vaultConfigured.value = false }
  finally { loading.value = false }
}
async function importSession() {
  if (busy.value || !vaultConfigured.value || !consented.value) return
  error.value = ''
  success.value = ''
  const extracted = extractSessionJSON(content.value)
  if (!name.value.trim() || !extracted) { error.value = t('supplier.invalidSession'); return }
  content.value = ''
  busy.value = true
  try {
    await isolatedSupplierAPI.importResource({
      display_name: name.value.trim(), content: extracted, consent_version: 'all_available_continuous_v1',
      ...(updatingResource.value ? { resource_id: updatingResource.value.id, resource_version: updatingResource.value.version } : {})
    })
    closeImport()
    success.value = t('supplier.imported')
    await load()
  } catch (err) { error.value = message(err) }
  finally { content.value = ''; consented.value = false; busy.value = false }
}
async function action(resource: SupplierResource, action: 'usage' | 'pause' | 'disconnect' | 'start') {
  if (busy.value || importOpen.value || (action === 'start' && !canStart(resource))) return
  busy.value = true
  error.value = ''
  success.value = ''
  try {
    const updated = action === 'usage' ? await isolatedSupplierAPI.resourceUsage(resource.id, resource.version)
      : action === 'start' ? await isolatedSupplierAPI.startResource(resource.id, resource.version)
      : action === 'pause' ? await isolatedSupplierAPI.pauseResource(resource.id, resource.version)
        : await isolatedSupplierAPI.disconnectResource(resource.id, resource.version)
    resources.value = resources.value.map(item => item.id === updated.id ? updated : item)
    disconnecting.value = null
    if (action === 'pause') success.value = t('supplier.pausedNotice')
  } catch (err) { error.value = message(err) }
  finally { busy.value = false }
}
let listTimer: ReturnType<typeof setInterval> | undefined
async function refreshList() {
  if (busy.value || loading.value || importOpen.value || document.visibilityState === 'hidden') return
  try {
    const data = await isolatedSupplierAPI.resources()
    resources.value = data.items || []
    vaultConfigured.value = data.vault_configured
    listRefreshFailed.value = false
  } catch { listRefreshFailed.value = true }
}
onMounted(() => {
  void load()
  listTimer = setInterval(() => { void refreshList() }, 30000)
})
onUnmounted(() => clearInterval(listTimer))
</script>
