<template>
  <AppLayout>
    <div class="space-y-6">
      <section class="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.suppliers.title') }}</h2>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.suppliers.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="loading || !!controlling" @click="loadResources">
          {{ t('common.refresh') }}
        </button>
      </section>

      <div class="rounded border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-800 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-200">
        {{ t('admin.suppliers.inventoryNotice') }}
      </div>

      <section class="space-y-3 rounded border border-gray-200 p-4 dark:border-dark-700">
        <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.suppliers.settingsTitle') }}</h3>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.suppliers.admissionNotice') }}</p>
        <p v-if="settingsLoading">{{ t('common.loading') }}</p>
        <div v-else-if="settingsFailed" role="alert">
          <p>{{ t('admin.suppliers.settingsError') }}</p>
          <button type="button" class="btn btn-secondary mt-2" @click="loadSettings">{{ t('common.refresh') }}</button>
        </div>
        <form v-else-if="settings" class="space-y-4" @submit.prevent="saveSettings">
          <label class="flex items-center gap-2 text-sm"><input v-model="settings.auto_admission" type="checkbox" :disabled="saving" />{{ t('admin.suppliers.autoAdmission') }}</label>
          <label class="block text-sm">
            <span>{{ t('admin.suppliers.targetGroups') }}</span>
            <select v-model="settings.group_ids" multiple class="input mt-1 block w-full" :disabled="saving">
              <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }} (#{{ group.id }})</option>
              <option v-for="id in missingGroupIds" :key="id" :value="id">#{{ id }} — {{ t('admin.suppliers.unavailableGroup') }}</option>
            </select>
          </label>
          <p class="text-xs text-gray-500">{{ t('admin.suppliers.groupHint') }}</p>
          <label class="block text-sm">
            <span>{{ t('admin.suppliers.feeMode') }}</span>
            <select v-model="settings.fee_mode" class="input mt-1 block w-full" :disabled="saving">
              <option value="standard_cost">{{ t('admin.suppliers.standardCost') }}</option>
              <option value="customer_charge">{{ t('admin.suppliers.customerCharge') }}</option>
            </select>
          </label>
          <label class="block text-sm">
            <span>{{ t('admin.suppliers.feePercent') }}</span>
            <input v-model.number="feePercent" type="number" min="0" max="100" step="0.01" required class="input mt-1 block w-full" :disabled="saving" />
          </label>
          <p v-if="saveMessage" role="status" class="text-sm">{{ saveMessage }}</p>
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="loadSettings">{{ t('admin.suppliers.reloadSettings') }}</button>
          <button type="submit" class="btn btn-primary" :disabled="saving || !validSettings">{{ saving ? t('common.submitting') : t('common.save') }}</button>
        </form>
      </section>

      <p v-if="controlMessage" role="status" class="text-sm">{{ controlMessage }}</p>
      <div :aria-busy="loading" aria-live="polite">
        <p v-if="loading" class="py-12 text-center text-gray-500">{{ t('common.loading') }}</p>
        <p v-else-if="failed" role="alert" class="py-12 text-center text-red-600 dark:text-red-400">
          {{ t('admin.suppliers.loadError') }}
        </p>
        <p v-else-if="items.length === 0" class="py-12 text-center text-gray-500">{{ t('admin.suppliers.empty') }}</p>
        <div v-else class="overflow-x-auto rounded border border-gray-200 dark:border-dark-700">
          <table class="min-w-full divide-y divide-gray-200 text-left text-sm dark:divide-dark-700">
            <caption class="sr-only">{{ t('admin.suppliers.title') }}</caption>
            <thead class="bg-gray-50 text-gray-600 dark:bg-dark-800 dark:text-gray-300">
              <tr>
                <th v-for="column in columns" :key="column" scope="col" class="whitespace-nowrap px-4 py-3 font-medium">{{ t(`admin.suppliers.${column}`) }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 text-gray-900 dark:divide-dark-700 dark:text-gray-200">
              <tr v-for="item in items" :key="`${item.supplier_user_id}:${item.resource.id}`">
                <td class="px-4 py-3">
                  <div>{{ item.supplier_name || t('admin.suppliers.unknown') }}</div>
                  <div class="text-xs text-gray-500">{{ t('admin.suppliers.supplierId') }}: {{ item.supplier_user_id }}</div>
                </td>
                <td class="px-4 py-3">
                  <div>{{ item.resource.display_name || t('admin.suppliers.unknown') }}</div>
                  <div class="text-xs text-gray-500">{{ item.resource.id }}</div>
                </td>
                <td class="whitespace-nowrap px-4 py-3">{{ item.resource.masked_email || t('admin.suppliers.unknown') }}</td>
                <td class="px-4 py-3">{{ item.resource.plan_type || t('admin.suppliers.unknown') }}</td>
                <td class="px-4 py-3">{{ stateLabel(item.resource.status) }}</td>
                <td class="whitespace-nowrap px-4 py-3" data-testid="admission">
                  <div>{{ t(item.resource.schedulable ? 'admin.suppliers.schedulable' : 'admin.suppliers.notSchedulable') }}</div>
                  <div class="text-xs text-gray-500">
                    <template v-if="item.resource.account_id != null">{{ t('admin.suppliers.accountId') }}: {{ item.resource.account_id }}</template>
                    <template v-else>{{ t('admin.suppliers.noAccount') }}</template>
                  </div>
                </td>
                <td class="px-4 py-3" data-testid="supply-control">
                  <p>{{ t(typeof item.resource.admin_disabled !== 'boolean' ? 'admin.suppliers.unknown' : item.resource.admin_disabled ? 'admin.suppliers.supplyProhibited' : 'admin.suppliers.supplyNotProhibited') }}</p>
                  <p class="text-xs text-gray-500">{{ t(typeof item.resource.supply_enabled !== 'boolean' ? 'admin.suppliers.unknown' : item.resource.supply_enabled ? 'admin.suppliers.supplierSupplyEnabled' : 'admin.suppliers.supplierSupplyPaused') }}</p>
                  <button type="button" class="btn btn-secondary mt-2" :disabled="loading || !!controlling || !hasSupplyControl(item.resource)" @click="toggleSupplyControl(item.resource)">
                    {{ t(item.resource.admin_disabled ? 'admin.suppliers.removeProhibition' : 'admin.suppliers.prohibitSupply') }}
                  </button>
                </td>
                <td class="px-4 py-3">{{ stateLabel(item.resource.probe_state) }}</td>
                <td class="whitespace-nowrap px-4 py-3">{{ dateLabel(item.resource.token_expires_at) }}</td>
                <td class="whitespace-nowrap px-4 py-3">{{ dateLabel(item.resource.last_probe_at) }}</td>
                <td class="whitespace-nowrap px-4 py-3">
                  <template v-if="item.resource.usage">
                    <div v-for="window in usageWindows" :key="window">
                      {{ t(`admin.suppliers.${window}`) }}: {{ usageLabel(item.resource.usage.rate_limit?.[window]?.used_percent) }}
                      <span v-if="item.resource.usage.rate_limit?.[window]?.reset_at"> · {{ t('admin.suppliers.resetsAt') }} {{ epochLabel(item.resource.usage.rate_limit?.[window]?.reset_at) }}</span>
                    </div>
                    <div class="text-xs text-gray-500">{{ t('admin.suppliers.fetchedAt') }}: {{ epochLabel(item.resource.usage.fetched_at) }}</div>
                  </template>
                  <span v-else>{{ t('admin.suppliers.unknown') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { getSupplierSettings, saveSupplierSettings, listSupplierResources, setSupplierSupplyControl, type AdminSupplierResource, type SupplierSettings } from '@/api/admin/suppliers'
import { getAll } from '@/api/admin/groups'
import type { AdminGroup } from '@/types'
import { formatDateTime } from '@/utils/format'

const { t, te, locale } = useI18n()
const items = ref<AdminSupplierResource[]>([])
const loading = ref(false)
const failed = ref(false)
const controlling = ref<string | null>(null)
const controlMessage = ref('')
function hasSupplyControl(resource: AdminSupplierResource['resource']) {
  return Number.isInteger(resource.version) && resource.version > 0 && typeof resource.admin_disabled === 'boolean'
}
async function toggleSupplyControl(resource: AdminSupplierResource['resource']) {
  if (loading.value || controlling.value || !hasSupplyControl(resource)) return
  controlling.value = resource.id
  controlMessage.value = ''
  try {
    const updated = await setSupplierSupplyControl(resource.id, resource.version, !resource.admin_disabled)
    items.value = items.value.map(item => item.resource.id === updated.id ? { ...item, resource: updated } : item)
    await loadResources()
    controlMessage.value = t(failed.value ? 'admin.suppliers.controlReloadFailed' : 'admin.suppliers.controlSaved')
  } catch (error) {
    const status = (error as { response?: { status?: number } })?.response?.status
    controlMessage.value = t(status === 409 ? 'admin.suppliers.controlConflict' : 'admin.suppliers.controlError')
  } finally {
    controlling.value = null
  }
}
const columns = ['supplier', 'resource', 'maskedEmail', 'plan', 'status', 'admission', 'supplyControl', 'probeState', 'tokenExpiresAt', 'lastProbeAt', 'usage']
const usageWindows = ['primary_window', 'secondary_window'] as const

function usageLabel(value: number | null | undefined): string {
  return typeof value === 'number' && Number.isFinite(value) ? `${value}%` : t('admin.suppliers.unknown')
}

function epochLabel(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return t('admin.suppliers.unknown')
  const date = new Date(value * 1000)
  return Number.isFinite(date.getTime()) ? dateLabel(date.toISOString()) : t('admin.suppliers.unknown')
}

function stateLabel(state: string): string {
  const key = `admin.suppliers.states.${state}`
  return te(key) ? t(key) : state || t('admin.suppliers.unknown')
}

function dateLabel(value: string | null): string {
  if (!value || !Number.isFinite(Date.parse(value))) return t('admin.suppliers.unknown')
  return formatDateTime(value, undefined, locale.value)
}

async function loadResources() {
  if (loading.value) return
  loading.value = true
  failed.value = false
  try {
    items.value = await listSupplierResources()
  } catch {
    items.value = []
    failed.value = true
  } finally {
    loading.value = false
  }
}

const settings = ref<SupplierSettings | null>(null)
const groups = ref<AdminGroup[]>([])
const settingsLoading = ref(false)
const settingsFailed = ref(false)
const saving = ref(false)
const saveMessage = ref('')
const feePercent = ref<number | string>(0)
const missingGroupIds = computed(() => settings.value?.group_ids.filter(id => !groups.value.some(group => group.id === id)) ?? [])
const validSettings = computed(() => typeof feePercent.value === 'number' && Number.isFinite(feePercent.value)
  && feePercent.value >= 0 && feePercent.value <= 100 && (settings.value?.group_ids.length ?? 0) <= 100
  && missingGroupIds.value.length === 0 && (!settings.value?.auto_admission || settings.value.group_ids.length > 0))

async function loadSettings() {
  if (settingsLoading.value) return
  settingsLoading.value = true
  settingsFailed.value = false
  try {
    const [policy, availableGroups] = await Promise.all([getSupplierSettings(), getAll()])
    settings.value = policy
    feePercent.value = policy.fee_ratio_bps / 100
    groups.value = availableGroups.filter(group => group.status === 'active' && ['openai', 'composite'].includes(group.platform))
  } catch {
    settingsFailed.value = true
  } finally {
    settingsLoading.value = false
  }
}

async function saveSettings() {
  if (!settings.value || !validSettings.value || saving.value) return
  saving.value = true
  saveMessage.value = ''
  try {
    await saveSupplierSettings({ ...settings.value, fee_ratio_bps: Math.round(Number(feePercent.value) * 100) })
    await loadSettings()
    saveMessage.value = t(settingsFailed.value ? 'admin.suppliers.savedReloadFailed' : 'admin.suppliers.settingsSaved')
  } catch (error) {
    const status = (error as { response?: { status?: number } })?.response?.status
    saveMessage.value = t(status === 409 ? 'admin.suppliers.settingsConflict' : 'admin.suppliers.saveError')
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void loadResources()
  void loadSettings()
})
</script>
