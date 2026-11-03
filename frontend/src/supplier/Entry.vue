<template>
  <section class="card space-y-6 p-6">
    <p>{{ t('supplier.intro') }}</p>
    <RouterLink v-if="supplierSession.user" to="/supplier/portal" class="btn btn-primary">{{ t('supplier.openPortal') }}</RouterLink>
    <div v-else class="flex flex-wrap gap-3">
      <RouterLink :to="{ path: '/register', query: { redirect: '/supplier/portal' } }" class="btn btn-primary">{{ t('supplier.signUp') }}</RouterLink>
      <RouterLink :to="{ path: '/login', query: { redirect: '/supplier/portal' } }" class="btn btn-secondary">{{ t('supplier.signIn') }}</RouterLink>
    </div>
  </section>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { supplierSession } from './session'
const { t } = useI18n()
</script>
