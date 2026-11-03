<template>
  <section class="card space-y-5 p-6">
    <h1 class="text-xl font-semibold">{{ t(registering ? 'supplier.signUp' : 'supplier.signIn') }}</h1>
    <p v-if="loading" role="status">{{ t('supplier.loading') }}</p>
    <div v-else-if="configFailed"><p role="alert">{{ t('supplier.configFailed') }}</p><button class="btn btn-secondary" @click="loadSettings">{{ t('supplier.retry') }}</button></div>
    <p v-else-if="blocked" role="alert">{{ t('supplier.securityUnsupported') }}</p>
    <p v-else-if="registering && !settings?.registration_enabled" role="alert">{{ t('supplier.registrationDisabled') }}</p>
    <p v-else-if="registering && settings?.email_delivery_configured !== true" role="alert">{{ t('supplier.smtpUnavailable') }} {{ t('supplier.mailConfigOnly') }}</p>
    <form v-else class="space-y-4" @submit.prevent="submit">
      <template v-if="!tempToken">
        <label class="block" for="email">{{ t('supplier.email') }}</label>
        <input id="email" v-model="email" class="input" type="email" autocomplete="username" required :disabled="busy" />
        <label class="block" for="password">{{ t('supplier.password') }}</label>
        <input id="password" v-model="password" class="input" type="password" :autocomplete="registering ? 'new-password' : 'current-password'" required :disabled="busy" />
        <template v-if="registering">
          <label class="block" for="confirm-password">{{ t('supplier.confirmPassword') }}</label>
          <input id="confirm-password" v-model="confirmation" class="input" type="password" autocomplete="new-password" required :disabled="busy" />
          <button type="button" class="btn btn-secondary" :disabled="busy || cooldown > 0 || !email" @click="sendCode">
            {{ cooldown > 0 ? t('supplier.resendIn', { seconds: cooldown }) : t('supplier.sendCode') }}
          </button>
          <p v-if="codeSent" role="status">{{ t('supplier.codeSent') }}</p>
        </template>
      </template>
      <template v-if="registering || tempToken">
        <label class="block" for="code">{{ t(tempToken ? 'supplier.totpCode' : 'supplier.verifyCode') }}</label>
        <input id="code" v-model="code" class="input" required pattern="[0-9]{6}" maxlength="6" inputmode="numeric" autocomplete="one-time-code" :disabled="busy" />
      </template>
      <p v-if="error" role="alert" class="text-red-600 dark:text-red-400">{{ error }}</p>
      <button class="btn btn-primary" type="submit" :disabled="busy">{{ t(busy ? 'supplier.working' : registering ? 'supplier.signUp' : 'supplier.signIn') }}</button>
    </form>
    <RouterLink :to="{ path: registering ? '/login' : '/register', query: { redirect: supplierReturn(route.query.redirect) } }" class="block text-primary-600 dark:text-primary-400">
      {{ t(registering ? 'supplier.signIn' : 'supplier.signUp') }}
    </RouterLink>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { isolatedSupplierAPI, type SupplierSettings } from './api'
import { saveSupplierSession, supplierSession } from './session'
import { supplierReturn } from './router'
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const registering = computed(() => route.path === '/register')
const settings = ref<SupplierSettings | null>(null)
const loading = ref(true)
const configFailed = ref(false)
// Never silently omit a configured proof or legal consent requirement.
const blocked = computed(() => settings.value?.supplier_portal !== true || settings.value?.turnstile_enabled || settings.value?.tencent_captcha_enabled || settings.value?.aliyun_captcha_enabled || settings.value?.login_agreement_enabled)
const email = ref('')
const password = ref('')
const confirmation = ref('')
const code = ref('')
const tempToken = ref('')
const busy = ref(false)
const error = ref('')
const cooldown = ref(0)
const codeSent = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
onUnmounted(() => clearInterval(timer))
async function loadSettings() {
  loading.value = true
  configFailed.value = false
  try { settings.value = await isolatedSupplierAPI.settings() } catch { configFailed.value = true }
  finally { loading.value = false }
}
onMounted(loadSettings)
function startCooldown() {
  cooldown.value = 60
  clearInterval(timer)
  timer = setInterval(() => { if (--cooldown.value <= 0) clearInterval(timer) }, 1000)
}
async function sendCode() {
  if (busy.value || blocked.value || !settings.value?.registration_enabled || settings.value?.email_delivery_configured !== true || cooldown.value) return
  busy.value = true
  error.value = ''
  try { await isolatedSupplierAPI.sendRegistrationCode(email.value.trim()); codeSent.value = true; startCooldown() }
  catch { error.value = t('supplier.sendFailed'); startCooldown() }
  finally { busy.value = false }
}
async function submit() {
  if (busy.value || loading.value || configFailed.value || blocked.value || (registering.value && (!settings.value?.registration_enabled || settings.value?.email_delivery_configured !== true))) return
  if (registering.value && password.value !== confirmation.value) { error.value = t('supplier.passwordMismatch'); return }
  busy.value = true
  error.value = ''
  try {
    const data = tempToken.value
      ? await isolatedSupplierAPI.login2FA(tempToken.value, code.value)
      : registering.value
        ? await isolatedSupplierAPI.register(email.value.trim(), password.value, code.value)
        : await isolatedSupplierAPI.login(email.value.trim(), password.value)
    password.value = ''
    confirmation.value = ''
    code.value = ''
    if ('requires_2fa' in data) { tempToken.value = data.temp_token; return }
    saveSupplierSession(data)
    await isolatedSupplierAPI.restore()
    if (!supplierSession.user) throw new Error('Unable to restore supplier identity')
    await router.replace(supplierReturn(route.query.redirect))
  } catch { error.value = t('supplier.authFailed') }
  finally { busy.value = false }
}
</script>
