<template>
  <section class="card min-w-0 space-y-5 p-4 sm:p-6">
    <h2 class="text-xl font-semibold">{{ t('supplier.resourcesTitle') }}</h2>
    <p>{{ t('supplier.resourceBoundary') }}</p>
    <div class="flex flex-wrap gap-3">
      <button class="btn btn-primary" :disabled="busy || !vaultConfigured" @click="openImport">{{ t('supplier.importSession') }}</button>
      <button class="btn btn-secondary" :disabled="busy || loading" @click="load">{{ t('supplier.reloadResources') }}</button>
    </div>
    <p v-if="!loading && !loadFailed && !vaultConfigured" role="status">{{ t('supplier.importUnconfigured') }}</p>
    <form v-if="importOpen" class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-700" @submit.prevent="importSession">
      <p>{{ t('supplier.sessionSafety') }}</p>
      <label for="resource-name" class="block">{{ t('supplier.displayName') }}</label>
      <input id="resource-name" v-model="name" class="input" required maxlength="100" autocomplete="off" :disabled="busy" />
      <label for="resource-session" class="block">{{ t('supplier.sessionJSON') }}</label>
      <textarea id="resource-session" v-model="content" class="input min-h-40" required autocomplete="off" autocapitalize="off" spellcheck="false" data-private="true" :disabled="busy" />
      <div class="flex flex-wrap gap-3">
        <button type="submit" class="btn btn-primary" :disabled="busy">{{ t(busy ? 'supplier.working' : 'supplier.importSession') }}</button>
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
        <p>{{ t(resource.status === 'disconnected' ? 'supplier.disconnected' : 'supplier.paused') }} · {{ t('supplier.notSchedulable') }}</p>
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
        <div v-if="resource.status !== 'disconnected'" class="flex flex-wrap gap-3">
          <button class="btn btn-secondary" :disabled="busy" @click="action(resource, 'usage')">{{ t('supplier.refreshUsage') }}</button>
          <button class="btn btn-secondary" :disabled="busy || !resource.auto_sync_enabled" @click="action(resource, 'pause')">{{ t('supplier.pauseSync') }}</button>
          <button v-if="!resource.auto_sync_enabled && resource.probe_state !== 'reauthorization_required'" class="btn btn-secondary" :disabled="busy || !vaultConfigured" @click="action(resource, 'resume')">{{ t('supplier.resumeSync') }}</button>
          <button v-if="resource.probe_state === 'reauthorization_required'" class="btn btn-primary" :disabled="busy || !vaultConfigured" @click="reconnect(resource)">{{ t('supplier.reconnect') }}</button>
          <button class="btn btn-secondary" :disabled="busy" @click="disconnecting = resource.id">{{ t('supplier.disconnect') }}</button>
        </div>
        <div v-if="disconnecting === resource.id" class="space-y-2">
          <p>{{ t('supplier.disconnectConfirm') }}</p>
          <button class="btn btn-danger" :disabled="busy" @click="action(resource, 'disconnect')">{{ t('supplier.confirmDisconnect') }}</button>
          <button class="btn btn-secondary" :disabled="busy" @click="disconnecting = null">{{ t('supplier.cancel') }}</button>
        </div>
      </li>
    </ul>
  </section>
</template>
<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { isolatedSupplierAPI, supplierErrorReason } from './api'
import { validSessionJSON, type SupplierResource } from './resourceTypes'
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
const reconnecting = ref<string | null>(null)
function closeImport() { content.value = ''; name.value = ''; importOpen.value = false }
function openImport() { closeImport(); error.value = ''; success.value = ''; importOpen.value = true }
onUnmounted(closeImport)
function syncState(state: string) {
  const states: Record<string, string> = { pending: 'syncPending', running: 'syncRunning', fresh: 'syncFresh', retrying: 'syncRetrying', paused: 'syncPaused', disconnected: 'disconnected', reauthorization_required: 'syncReauthorize' }
  return t(`supplier.${states[state] || 'unknown'}`)
}
function probeError(code: string) {
  const reasons: Record<string, string> = { expired: 'syncExpired', unauthorized: 'syncUnauthorized', forbidden: 'syncForbidden', rate_limited: 'syncRateLimited', upstream_unavailable: 'syncUnavailable', vault_unavailable: 'importUnconfigured' }
  return t(`supplier.${reasons[code] || 'syncFailedSafe'}`)
}
function reconnect(resource: SupplierResource) {
  reconnecting.value = resource.id
  disconnecting.value = resource.id
  error.value = t('supplier.reconnectInstructions')
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
  if (busy.value || !vaultConfigured.value) return
  error.value = ''
  success.value = ''
  if (!name.value.trim() || !validSessionJSON(content.value)) { error.value = t('supplier.invalidSession'); return }
  busy.value = true
  try {
    await isolatedSupplierAPI.importResource(name.value.trim(), content.value.trim())
    closeImport()
    success.value = t('supplier.imported')
    await load()
  } catch (err) { error.value = message(err) }
  finally { content.value = ''; busy.value = false }
}
async function action(resource: SupplierResource, action: 'usage' | 'pause' | 'disconnect' | 'resume') {
  if (busy.value) return
  busy.value = true
  error.value = ''
  success.value = ''
  try {
    const updated = action === 'usage' ? await isolatedSupplierAPI.resourceUsage(resource.id, resource.version)
      : action === 'resume' ? await isolatedSupplierAPI.resumeResourceSync(resource.id, resource.version)
      : action === 'pause' ? await isolatedSupplierAPI.pauseResource(resource.id, resource.version)
        : await isolatedSupplierAPI.disconnectResource(resource.id, resource.version)
    resources.value = resources.value.map(item => item.id === updated.id ? updated : item)
    disconnecting.value = null
    if (action === 'disconnect' && reconnecting.value === resource.id) {
      openImport()
      name.value = resource.display_name
      reconnecting.value = null
      success.value = t('supplier.reconnectReady')
    }
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
