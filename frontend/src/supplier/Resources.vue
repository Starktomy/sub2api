<template>
  <section class="min-w-0 space-y-5">
    <header class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h2 class="text-2xl font-semibold tracking-tight">{{ t('supplier.resourcesTitle') }}</h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('supplier.workspaceHint') }}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-primary" :disabled="busy || !vaultConfigured" @click="openImport">{{ t('supplier.authorizeAndStart') }}</button>
        <button class="btn btn-secondary" :disabled="busy || loading || importOpen" @click="load">{{ t('supplier.refreshList') }}</button>
      </div>
    </header>
    <p v-if="!loading && !loadFailed && !vaultConfigured" role="status" class="rounded-xl border border-gray-200 p-4 text-sm dark:border-dark-700">{{ t('supplier.importUnconfigured') }}</p>
    <form v-if="importOpen" class="card space-y-4 p-5 sm:p-6" @submit.prevent="importSession">
      <div>
        <h3 class="text-lg font-semibold">{{ t(updatingResource ? 'supplier.updateAuthorization' : 'supplier.importSession') }}</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t(updatingResource ? 'supplier.updateAuthorizationNotice' : 'supplier.importHint') }}</p>
      </div>
      <div>
        <label for="resource-name" class="mb-2 block text-sm font-medium">{{ t('supplier.displayName') }}</label>
        <input id="resource-name" v-model="name" class="input w-full" required maxlength="100" autocomplete="off" :disabled="busy" />
      </div>
      <div>
        <label for="resource-session" class="mb-2 block text-sm font-medium">{{ t('supplier.sessionJSON') }}</label>
        <textarea id="resource-session" v-model="content" class="input min-h-32 w-full font-mono text-sm" required autocomplete="off" autocapitalize="off" spellcheck="false" data-private="true" :disabled="busy" aria-describedby="session-safety" />
        <p id="session-safety" class="mt-2 text-xs text-gray-500 dark:text-dark-300">{{ t('supplier.sessionSafety') }}</p>
      </div>
      <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
        <p id="resource-consent" class="text-sm leading-relaxed">{{ t('supplier.supplyConsent') }}</p>
        <label class="mt-3 flex items-start gap-2 text-sm font-medium">
          <input id="resource-consent-check" v-model="consented" type="checkbox" required aria-describedby="resource-consent" :disabled="busy" class="mt-1" />
          <span>{{ t('supplier.confirmSupplyConsent') }}</span>
        </label>
      </div>
      <div class="flex flex-wrap gap-2">
        <button type="submit" class="btn btn-primary" :disabled="busy || !consented">{{ t(busy ? 'supplier.working' : 'supplier.authorizeAndStart') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="closeImport">{{ t('supplier.cancel') }}</button>
      </div>
    </form>
    <p v-if="error" role="alert" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">{{ error }}</p>
    <p v-if="success" role="status" class="rounded-xl border border-gray-200 bg-white p-4 text-sm dark:border-dark-700 dark:bg-dark-900">{{ success }}</p>
    <p v-if="listRefreshFailed" role="status" class="text-sm text-gray-500 dark:text-dark-300">{{ t('supplier.syncDisplayStale') }}</p>
    <p v-if="loading" role="status" class="py-12 text-center text-gray-500">{{ t('supplier.loadingResources') }}</p>
    <div v-else-if="loadFailed" class="card space-y-3 p-8 text-center">
      <p role="alert">{{ t('supplier.resourcesFailed') }}</p>
      <button class="btn btn-secondary" :disabled="busy" @click="load">{{ t('supplier.retry') }}</button>
    </div>
    <div v-else-if="!resources.length" class="card space-y-3 px-6 py-12 text-center">
      <h3 class="text-lg font-semibold">{{ t('supplier.noResources') }}</h3>
      <p class="mx-auto max-w-md text-sm leading-relaxed text-gray-500 dark:text-dark-300">{{ t('supplier.emptyHint') }}</p>
    </div>
    <ul v-else class="space-y-5">
      <li v-for="resource in resources" :key="resource.id" class="card min-w-0 overflow-hidden">
        <div class="space-y-4 p-5 sm:p-6">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0 break-words">
              <h3 class="text-lg font-semibold">{{ resource.display_name }}</h3>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ resource.email || resource.masked_email || t('supplier.unknown') }} · {{ resource.usage?.plan_type || resource.plan_type || t('supplier.unknown') }}</p>
            </div>
            <div class="text-right">
              <span class="inline-flex rounded-full border border-gray-200 px-3 py-1 text-xs font-medium dark:border-dark-600">{{ supplyState(resource) }}</span>
              <p class="mt-1 text-xs text-gray-500 dark:text-dark-300">{{ t(resource.schedulable ? 'supplier.schedulable' : 'supplier.notSchedulable') }}</p>
            </div>
          </div>
          <p v-if="resourceNotice(resource)" role="status" class="rounded-lg bg-gray-50 px-4 py-3 text-sm leading-relaxed dark:bg-dark-900">{{ resourceNotice(resource) }}</p>
          <ResourceStats :resource-id="resource.id" />
          <div class="flex flex-wrap gap-2">
            <template v-if="resource.status !== 'disconnected'">
              <button v-if="resource.supply_enabled || resource.availability_reason === 'verification_required'" class="btn btn-secondary" :disabled="busy || importOpen" @click="action(resource, 'pause')">{{ t('supplier.pauseSupply') }}</button>
              <button v-else class="btn btn-primary" :disabled="busy || importOpen || !canStart(resource)" @click="action(resource, 'start')">{{ t('supplier.startSupply') }}</button>
            </template>
            <button class="btn btn-secondary" :disabled="busy || importOpen || !vaultConfigured" @click="updateAuthorization(resource)">{{ t('supplier.updateAuthorization') }}</button>
          </div>
        </div>
        <details class="border-t border-gray-100 dark:border-dark-700">
          <summary class="cursor-pointer px-5 py-3 text-sm font-medium text-gray-600 focus-visible:outline focus-visible:outline-2 sm:px-6 dark:text-dark-300">{{ t('supplier.resourceDetails') }}</summary>
          <div class="space-y-4 px-5 pb-5 text-sm sm:px-6">
            <div class="grid gap-3 sm:grid-cols-2">
              <p>{{ t(resource.usage ? 'supplier.observedPlan' : 'supplier.declaredPlan') }}: {{ resource.usage?.plan_type || resource.plan_type || t('supplier.unknown') }}</p>
              <p>{{ t('supplier.authorizationExpiry') }}: {{ date(resource.authorization_expires_at) }}</p>
              <p>{{ t('supplier.tokenExpiry') }}: {{ date(resource.token_expires_at) }}</p>
              <p>{{ t('supplier.updatedAt') }}: {{ date(resource.updated_at) }}</p>
            </div>
            <dl class="grid gap-3 sm:grid-cols-2">
              <div v-for="window in ['primary_window', 'secondary_window'] as const" :key="window" class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900">
                <dt class="text-gray-500 dark:text-dark-300">{{ t(window === 'primary_window' ? 'supplier.primaryWindow' : 'supplier.secondaryWindow') }}</dt>
                <dd class="mt-1 font-medium">{{ percent(resource.usage?.rate_limit?.[window]?.used_percent) }}</dd>
                <dd class="mt-1 text-xs text-gray-500 dark:text-dark-300">{{ t('supplier.resetsAt') }}: {{ unixDate(resource.usage?.rate_limit?.[window]?.reset_at) }}</dd>
              </div>
            </dl>
            <p class="text-xs text-gray-500 dark:text-dark-300">{{ t('supplier.usageUpdatedAt') }}: {{ date(resource.last_usage_at) }} · {{ t(resource.usage_state === 'fresh' ? 'supplier.snapshot' : resource.usage_state === 'stale' ? 'supplier.stale' : 'supplier.unknown') }}</p>
            <div class="space-y-1 text-xs text-gray-500 dark:text-dark-300">
              <p>{{ t('supplier.autoSync') }}: {{ t(resource.auto_sync_enabled ? 'supplier.syncRequested' : 'supplier.syncDisabled') }} · {{ syncState(resource.probe_state) }}</p>
              <p>{{ t('supplier.lastProbe') }}: {{ date(resource.last_probe_at) }} · {{ t('supplier.nextProbe') }}: {{ date(resource.next_probe_at) }}</p>
              <p>{{ t('supplier.syncWorkerCaveat') }}</p>
            </div>
            <div v-if="resource.status !== 'disconnected'" class="flex flex-wrap gap-2 border-t border-gray-100 pt-4 dark:border-dark-700">
              <button class="btn btn-secondary" :disabled="busy || importOpen" @click="action(resource, 'usage')">{{ t('supplier.refreshUsage') }}</button>
              <button class="btn btn-secondary" :disabled="busy || importOpen" @click="disconnecting = resource.id">{{ t('supplier.disconnect') }}</button>
            </div>
            <div v-if="disconnecting === resource.id" class="space-y-3 rounded-lg border border-red-200 p-4 dark:border-red-900">
              <p>{{ t('supplier.disconnectConfirm') }}</p>
              <div class="flex flex-wrap gap-2">
                <button class="btn btn-danger" :disabled="busy" @click="action(resource, 'disconnect')">{{ t('supplier.confirmDisconnect') }}</button>
                <button class="btn btn-secondary" :disabled="busy" @click="disconnecting = null">{{ t('supplier.cancel') }}</button>
              </div>
            </div>
          </div>
        </details>
      </li>
    </ul>
    <details class="text-sm text-gray-500 dark:text-dark-300">
      <summary class="cursor-pointer font-medium">{{ t('supplier.supplyRules') }}</summary>
      <p class="mt-2 max-w-3xl leading-relaxed">{{ t('supplier.resourceBoundary') }}</p>
    </details>
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
  const reasons: Record<string, string> = { phone_verification_required: 'phoneVerificationRequired', identity_email_mismatch: 'identityEmailMismatch', identity_unknown: 'identityUnknown', expired: 'syncExpired', unauthorized: 'syncUnauthorized', forbidden: 'syncForbidden', rate_limited: 'syncRateLimited', upstream_unavailable: 'syncUnavailable', vault_unavailable: 'importUnconfigured' }
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
    phone_verification_required: 'phoneVerificationRequired', identity_email_mismatch: 'identityEmailMismatch', identity_unknown: 'identityUnknown',
    verification_required: 'verificationRequired', quota_unknown: 'quotaUnknown',
    quota_exhausted: 'quotaExhausted', quota_stale: 'quotaStale', unavailable: 'availabilityPending'
  }
  return t(`supplier.${reasons[reason] || 'availabilityPending'}`)
}
function resourceNotice(resource: SupplierResource) {
  if (resource.admin_disabled) return t('supplier.adminDisabledNotice')
  if (resource.availability_reason) return availabilityReason(resource.availability_reason)
  if (resource.probe_error_code) return probeError(resource.probe_error_code)
  return ''
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
