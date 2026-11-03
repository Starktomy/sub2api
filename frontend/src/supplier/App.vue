<template>
  <main class="min-h-screen bg-gray-50 px-4 py-12 text-gray-900 dark:bg-dark-950 dark:text-white">
    <div class="mx-auto max-w-xl space-y-6">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <RouterLink to="/supplier" class="text-xl font-bold">{{ t('supplier.title') }}</RouterLink>
        <button v-if="supplierSession.user" class="btn btn-secondary" :disabled="signingOut" @click="logout">{{ t('supplier.signOut') }}</button>
      </header>
      <RouterView :key="route.path" />
      <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('supplier.scope') }}</p>
    </div>
  </main>
</template>
<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { supplierSession } from './session'
import { isolatedSupplierAPI } from './api'
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const signingOut = ref(false)
async function logout() {
  signingOut.value = true
  try { await isolatedSupplierAPI.logout() } catch { /* Local session is cleared even if revocation fails. */ }
  finally { signingOut.value = false; await router.replace('/supplier') }
}
</script>
