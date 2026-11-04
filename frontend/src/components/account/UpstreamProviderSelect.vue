<template>
  <div data-testid="upstream-provider-binding">
    <label class="input-label" :for="id">{{ t('admin.accounts.upstreamProvider.label') }}</label>
    <select :id="id" class="input" :value="modelValue" :disabled="loading"
      @change="$emit('update:modelValue', ($event.target as HTMLSelectElement).value)">
      <option value="">{{ t('admin.accounts.upstreamProvider.legacy') }}</option>
      <option v-if="unknownSelection" :value="modelValue" disabled>
        {{ t('admin.accounts.upstreamProvider.unknown') }} ({{ String(modelValue) }})
      </option>
      <option v-for="provider in options" :key="provider.id" :value="provider.id">{{ provider.display_name || provider.id }}</option>
    </select>
    <p class="input-hint">{{ t('admin.accounts.upstreamProvider.hint') }}</p>
    <ul v-if="conditions.length" data-testid="upstream-provider-conditions" class="list-inside list-disc text-xs text-gray-500 dark:text-gray-400">
      <li v-for="condition in conditions" :key="condition">{{ condition }}</li>
    </ul>
    <p v-if="failed" role="alert" class="text-sm text-red-600">{{ t('admin.accounts.upstreamProvider.unavailable') }}</p>
    <p v-if="unknownSelection" role="alert" class="text-sm text-red-600">{{ t('admin.accounts.upstreamProvider.invalid') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UpstreamProviderDescriptor } from '@/api/admin/upstreamProviders'
const props = defineProps<{
  id: string
  modelValue: unknown
  options: UpstreamProviderDescriptor[]
  loading: boolean
  failed: boolean
  unknownSelection: boolean
}>()
defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const conditions = computed(() => {
  const values = props.options.find(provider => provider.id === props.modelValue)?.conditions
  return Array.isArray(values) ? values.filter((value): value is string => typeof value === 'string') : []
})
</script>
