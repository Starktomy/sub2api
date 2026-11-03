<template>
  <div class="space-y-6">
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
        <Resources />
      </section>
      <form v-else class="card space-y-5 p-6" @submit.prevent="enroll">
        <h2 class="text-xl font-semibold">{{ t('supplier.enrollTitle') }}</h2>
        <p>{{ t('supplier.verifyDescription') }}</p>
        <p v-if="mailLoading" role="status">{{ t('supplier.loading') }}</p>
        <div v-else-if="mailFailed"><p role="alert">{{ t('supplier.configFailed') }}</p><button type="button" class="btn btn-secondary" @click="loadMailSettings">{{ t('supplier.retry') }}</button></div>
        <p v-else-if="!mailConfigured" role="alert">{{ t('supplier.smtpUnavailable') }} {{ t('supplier.mailConfigOnly') }}</p>
        <div>
          <label for="supplier-name" class="mb-2 block font-medium">{{ t('supplier.displayName') }}</label>
          <input id="supplier-name" v-model="displayName" class="input w-full" required maxlength="100" autocomplete="nickname" :disabled="enrolling" />
        </div>
        <button type="button" class="btn btn-secondary" :disabled="sending || enrolling || cooldown > 0 || !mailConfigured" @click="sendCode">
          {{ cooldown > 0 ? t('supplier.resendIn', { seconds: cooldown }) : t(sending ? 'supplier.sendingCode' : 'supplier.sendCode') }}
        </button>
        <p v-if="codeSent" role="status">{{ t('supplier.codeSent') }}</p>
        <div>
          <label for="supplier-code" class="mb-2 block font-medium">{{ t('supplier.verifyCode') }}</label>
          <input id="supplier-code" v-model="verifyCode" class="input w-full" required pattern="[0-9]{6}" maxlength="6" inputmode="numeric" autocomplete="one-time-code" :disabled="enrolling" />
        </div>
        <p v-if="error" role="alert" class="text-red-600 dark:text-red-400">{{ error }}</p>
        <button class="btn btn-primary" type="submit" :disabled="enrolling || sending || !mailConfigured || !displayName.trim() || !verifyCode.trim()">
          {{ t(enrolling ? 'supplier.enrolling' : 'supplier.enroll') }}
        </button>
      </form>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import Resources from './Resources.vue'
import { useI18n } from 'vue-i18n'
import { supplierSession } from './session'
import { isolatedSupplierAPI, supplierErrorReason } from './api'
import type { SupplierProfile } from '@/api/supplier'

const { t } = useI18n()
const authStore = supplierSession
const profile = ref<SupplierProfile | null>(null)
const displayName = ref('')
const verifyCode = ref('')
const loading = ref(true)
const loadFailed = ref(false)
const enrolling = ref(false)
const sending = ref(false)
const codeSent = ref(false)
const mailConfigured = ref(false)
const mailLoading = ref(true)
const mailFailed = ref(false)
async function loadMailSettings() {
  mailLoading.value = true
  mailFailed.value = false
  mailConfigured.value = false
  try { mailConfigured.value = (await isolatedSupplierAPI.settings()).email_delivery_configured === true }
  catch { mailFailed.value = true }
  finally { mailLoading.value = false }
}
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

const errorCode = supplierErrorReason

async function loadProfile() {
  loading.value = true
  loadFailed.value = false
  error.value = ''
  try {
    profile.value = await isolatedSupplierAPI.profile()
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
  if (sending.value || cooldown.value > 0 || !mailConfigured.value) return
  sending.value = true
  error.value = ''
  try {
    await isolatedSupplierAPI.sendCode()
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
  if (enrolling.value || !mailConfigured.value || !displayName.value.trim() || !/^[0-9]{6}$/.test(verifyCode.value.trim())) return
  enrolling.value = true
  error.value = ''
  try {
    profile.value = await isolatedSupplierAPI.enroll(displayName.value.trim(), verifyCode.value.trim())
    verifyCode.value = ''
  } catch (err) {
    error.value = t(errorCode(err) === 'SUPPLIER_EMAIL_VERIFICATION_REQUIRED' ? 'supplier.reverify' : 'supplier.enrollFailed')
    codeSent.value = false
  } finally {
    enrolling.value = false
  }
}

onMounted(() => { void loadProfile(); void loadMailSettings() })
</script>
