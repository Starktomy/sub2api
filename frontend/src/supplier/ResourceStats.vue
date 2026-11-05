<template>
  <section class="space-y-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-900" :aria-busy="loading">
    <div class="flex items-center justify-between gap-3">
      <h4 class="text-sm font-medium">{{ t('supplier.statsTitle') }}</h4>
      <button type="button" class="rounded px-2 py-1 text-xs text-gray-600 underline underline-offset-4 disabled:opacity-50 dark:text-dark-300" :disabled="loading" @click="load">{{ t('supplier.statsRefresh') }}</button>
    </div>
    <p v-if="loading" role="status">{{ t('supplier.statsLoading') }}</p>
    <p v-else-if="failed" role="alert">{{ t('supplier.statsError') }}</p>
    <dl v-else-if="stats" class="grid grid-cols-2 gap-x-4 gap-y-3 text-sm sm:grid-cols-3">
      <div v-for="field in tokenFields" :key="field" class="min-w-0">
        <dt class="text-xs text-gray-500 dark:text-dark-300">{{ t(`supplier.stats_${field}`) }}</dt>
        <dd class="mt-1 break-words font-medium tabular-nums">{{ stats[field] }}</dd>
      </div>
      <div class="order-first col-span-2 border-b border-gray-200 pb-3 sm:col-span-3 dark:border-dark-700">
        <dt class="text-xs text-gray-500 dark:text-dark-300">{{ t('supplier.statsFee') }}</dt>
        <dd class="mt-1 break-words text-2xl font-semibold tracking-tight tabular-nums">{{ stats.fee_amount }} {{ stats.currency }}</dd>
      </div>
    </dl>
    <p class="text-xs leading-relaxed text-gray-500 dark:text-dark-300">{{ t('supplier.statsNotice') }}</p>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isolatedSupplierAPI } from './api'
import type { SupplierResourceStats } from './resourceTypes'

const props = defineProps<{ resourceId: string }>()
const { t } = useI18n()
const stats = ref<SupplierResourceStats | null>(null)
const loading = ref(false)
const failed = ref(false)
const tokenFields = ['calls', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_creation_tokens'] as const
let requestId = 0

async function load() {
  const current = ++requestId
  loading.value = true
  failed.value = false
  stats.value = null
  try {
    const result = await isolatedSupplierAPI.resourceStats(props.resourceId)
    if (current === requestId) stats.value = result
  } catch {
    if (current === requestId) failed.value = true
  } finally {
    if (current === requestId) loading.value = false
  }
}

watch(() => props.resourceId, () => { void load() }, { immediate: true })
</script>
