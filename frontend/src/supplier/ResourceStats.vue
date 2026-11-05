<template>
  <section class="space-y-2 rounded-lg bg-gray-50 p-3 dark:bg-dark-900" :aria-busy="loading">
    <h4 class="font-semibold">{{ t('supplier.statsTitle') }}</h4>
    <p class="text-sm">{{ t('supplier.statsNotice') }}</p>
    <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">{{ t('supplier.statsRefresh') }}</button>
    <p v-if="loading" role="status">{{ t('supplier.statsLoading') }}</p>
    <p v-else-if="failed" role="alert">{{ t('supplier.statsError') }}</p>
    <dl v-else-if="stats" class="grid grid-cols-1 gap-2 text-sm sm:grid-cols-2">
      <div v-for="field in tokenFields" :key="field">
        <dt>{{ t(`supplier.stats_${field}`) }}</dt>
        <dd>{{ stats[field] }}</dd>
      </div>
      <div>
        <dt>{{ t('supplier.statsFee') }}</dt>
        <dd>{{ stats.fee_amount }} {{ stats.currency }}</dd>
      </div>
    </dl>
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
