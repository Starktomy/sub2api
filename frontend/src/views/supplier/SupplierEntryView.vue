<template>
  <main class="min-h-screen bg-gray-50 px-4 py-16 text-gray-900 dark:bg-dark-950 dark:text-white">
    <section class="card mx-auto max-w-xl space-y-6 p-8">
      <h1 class="text-2xl font-bold">{{ t('supplier.title') }}</h1>
      <p class="text-gray-600 dark:text-dark-300">{{ t('supplier.intro') }}</p>
      <p>{{ t('supplier.scope') }}</p>
      <div v-if="!authStore.isAuthenticated" class="flex flex-wrap gap-3">
        <RouterLink :to="authDestination('/register', destination)" class="btn btn-primary">{{ t('supplier.signUp') }}</RouterLink>
        <RouterLink :to="authDestination('/login', destination)" class="btn btn-secondary">{{ t('supplier.signIn') }}</RouterLink>
      </div>
      <RouterLink v-else :to="destination" class="btn btn-primary">{{ t('supplier.openPortal') }}</RouterLink>
      <RouterLink to="/home" class="block text-primary-600 dark:text-primary-400">{{ t('supplier.backHome') }}</RouterLink>
    </section>
  </main>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { authDestination } from '@/router/authRedirect'

const { t } = useI18n()
const authStore = useAuthStore()
const destination = '/supplier/portal'
</script>
