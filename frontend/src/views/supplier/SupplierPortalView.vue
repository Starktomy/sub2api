<template>
  <main class="min-h-screen bg-gray-50 px-4 py-12 text-gray-900 dark:bg-dark-950 dark:text-white">
    <div class="mx-auto max-w-2xl space-y-6">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-2xl font-bold">{{ t('supplier.title') }}</h1>
        <RouterLink to="/supplier" class="text-primary-600 dark:text-primary-400">{{ t('supplier.backEntry') }}</RouterLink>
      </header>
      <p class="text-gray-600 dark:text-dark-300">{{ authStore.user?.email }}</p>
      <p v-if="loading" role="status">{{ t('supplier.loading') }}</p>
      <section v-else-if="loadFailed" class="card space-y-4 p-6">
        <p role="alert">{{ t('supplier.loadFailed') }}</p>
        <button class="btn btn-secondary" @click="loadProfile">{{ t('supplier.retry') }}</button>
      </section>
      <section v-else-if="profile" class="card space-y-4 p-6">
        <h2 class="text-xl font-semibold">{{ profile.display_name }}</h2>
        <dl class="space-y-2">
          <dt class="text-gray-500 dark:text-dark-400">{{ t('supplier.verifiedEmail') }}</dt>
          <dd>{{ profile.verified_email }}</dd>
        </dl>
        <p>{{ t('supplier.enrolled') }}</p>
        <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
          <h3 class="font-semibold">{{ t('supplier.emptyTitle') }}</h3>
          <p class="mt-2 text-gray-600 dark:text-dark-300">{{ t('supplier.emptyDescription') }}</p>
        </div>
      </section>
      <form v-else class="card space-y-5 p-6" @submit.prevent="enroll">
        <h2 class="text-xl font-semibold">{{ t('supplier.enrollTitle') }}</h2>
        <p>{{ t('supplier.verifyDescription') }}</p>
        <div>
          <label for="supplier-name" class="mb-2 block font-medium">{{ t('supplier.displayName') }}</label>
          <input id="supplier-name" v-model="displayName" class="input w-full" required maxlength="100" autocomplete="nickname" :disabled="enrolling" />
        </div>
        <button type="button" class="btn btn-secondary" :disabled="sending || enrolling || cooldown > 0" @click="sendCode">
          {{ cooldown > 0 ? t('supplier.resendIn', { seconds: cooldown }) : t(sending ? 'supplier.sendingCode' : 'supplier.sendCode') }}
        </button>
        <p v-if="codeSent" role="status">{{ t('supplier.codeSent') }}</p>
        <div>
          <label for="supplier-code" class="mb-2 block font-medium">{{ t('supplier.verifyCode') }}</label>
          <input id="supplier-code" v-model="verifyCode" class="input w-full" required pattern="[0-9]{6}" maxlength="6" inputmode="numeric" autocomplete="one-time-code" :disabled="enrolling" />
        </div>
        <p v-if="error" role="alert" class="text-red-600 dark:text-red-400">{{ error }}</p>
        <button class="btn btn-primary" type="submit" :disabled="enrolling || sending || !displayName.trim() || !verifyCode.trim()">
          {{ t(enrolling ? 'supplier.enrolling' : 'supplier.enroll') }}
        </button>
      </form>
      <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('supplier.scope') }}</p>
    </div>
  </main>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { supplierAPI, type SupplierProfile } from '@/api/supplier'

const { t } = useI18n()
const authStore = useAuthStore()
const profile = ref<SupplierProfile | null>(null)
const displayName = ref(authStore.user?.username || '')
const verifyCode = ref('')
const loading = ref(true)
const loadFailed = ref(false)
const enrolling = ref(false)
const sending = ref(false)
const codeSent = ref(false)
const cooldown = ref(0)
let cooldownTimer: ReturnType<typeof setInterval> | undefined
function startCooldown() {
  cooldown.value = 60
  if (cooldownTimer) clearInterval(cooldownTimer)
  cooldownTimer = setInterval(() => {
    cooldown.value -= 1
    if (cooldown.value <= 0) clearInterval(cooldownTimer)
  }, 1000)
}
onUnmounted(() => clearInterval(cooldownTimer))
const error = ref('')

function errorCode(value: unknown): string | undefined {
  const error = value as { code?: string; reason?: string }
  return error?.reason || error?.code
}

async function loadProfile() {
  loading.value = true
  loadFailed.value = false
  error.value = ''
  try {
    profile.value = await supplierAPI.getProfile()
  } catch (err) {
    if (['NOT_ENROLLED', 'SUPPLIER_EMAIL_VERIFICATION_REQUIRED'].includes(errorCode(err) || '')) {
      profile.value = null
      if (errorCode(err) === 'SUPPLIER_EMAIL_VERIFICATION_REQUIRED') error.value = t('supplier.reverify')
    } else loadFailed.value = true
  } finally {
    loading.value = false
  }
}

async function sendCode() {
  if (sending.value || cooldown.value > 0) return
  sending.value = true
  error.value = ''
  try {
    await supplierAPI.sendVerifyCode()
    codeSent.value = true
    startCooldown()
  } catch (err) {
    if (errorCode(err) === 'VERIFY_CODE_TOO_FREQUENT') startCooldown()
    error.value = t('supplier.sendFailed')
  } finally {
    sending.value = false
  }
}

async function enroll() {
  if (enrolling.value || !displayName.value.trim() || !/^[0-9]{6}$/.test(verifyCode.value.trim())) return
  enrolling.value = true
  error.value = ''
  try {
    profile.value = await supplierAPI.enroll(displayName.value.trim(), verifyCode.value.trim())
    verifyCode.value = ''
  } catch (err) {
    error.value = t(errorCode(err) === 'SUPPLIER_EMAIL_VERIFICATION_REQUIRED' ? 'supplier.reverify' : 'supplier.enrollFailed')
    codeSent.value = false
  } finally {
    enrolling.value = false
  }
}

onMounted(loadProfile)
</script>
