<template>
  <BaseDialog v-if="auth.isAdmin" :show="show" :title="t(`${key}.title`)" width="wide" @close="close">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t(`${key}.notice`) }}</p>
      <button class="btn btn-secondary" data-testid="kiro-reload" :disabled="busy" @click="run()">{{ t(`${key}.reload`) }}</button>
      <p v-if="busy" role="status">{{ t(`${key}.loading`) }}</p>
      <p v-if="error" role="alert">{{ t(`${key}.failed`) }}</p>
      <p v-if="saved" role="status">{{ t(`${key}.saved`) }}</p>
      <template v-if="runtime">
        <div data-testid="kiro-runtime" role="status" class="space-y-2 text-sm">
          <div>{{ t(`${key}.version`) }}: {{ runtime.version }}</div>
          <div>{{ t(`${key}.region`) }}: {{ runtime.region === 'unknown' ? unknown : runtime.region }}</div>
          <div>{{ t(`${key}.mode`) }}: {{ runtime.authMode === 'unknown' ? unknown : runtime.authMode }}</div>
          <div>{{ runtime.paused === true ? t(`${key}.paused`) : runtime.paused === false ? t(`${key}.active`) : unknown }}</div>
          <div>{{ t(`${key}.refresh`) }}: {{ t(`${key}.${runtime.refreshState === 'unknown' ? 'unknown' : runtime.refreshState}`) }}</div>
          <div>{{ t(`${key}.${runtime.readiness === 'settlement_required' ? 'settlement' : runtime.readiness === 'ready' ? 'ready' : runtime.readiness === 'blocked' ? 'blocked' : runtime.readiness === 'refresh_required' ? 'refreshRequired' : 'unknown'}`) }}</div>
        </div>
        <button class="btn btn-secondary" data-testid="kiro-pause" :disabled="busy || uncertain" @click="mutate('pause')">{{ t(`${key}.pause`) }}</button>
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t(`${key}.queryNotice`) }}</p>
        <section v-for="kind in (['models', 'quota'] as const)" :key="kind" class="space-y-2 text-sm" :data-testid="`kiro-${kind}-snapshot`">
          <h3 class="font-medium">{{ t(`${key}.${kind}`) }}</h3>
          <p>{{ runtime[kind].state === 'known' ? t(`${key}.known`) : unknown }} — {{ t(`${key}.stale`) }}</p>
          <p>{{ t(`${key}.source`) }}: {{ t(`${key}.${runtime[kind].source === 'upstream' ? 'upstream' : runtime[kind].source === 'unsupported_reference' ? 'unsupported' : 'unknown'}`) }}</p>
          <p>{{ t(`${key}.observed`) }}: {{ runtime[kind].observedAt ?? unknown }}</p>
          <ul v-if="kind === 'models' && runtime.models.models.length"><li v-for="model in runtime.models.models" :key="model.id">{{ model.name ? `${model.name} (${model.id})` : model.id }}</li></ul>
          <ul v-if="kind === 'quota' && runtime.quota.quota.length"><li v-for="(row, index) in runtime.quota.quota" :key="index">{{ t(`${key}.used`) }}: {{ row.currentUsage ?? unknown }}; {{ t(`${key}.limit`) }}: {{ row.usageLimit ?? unknown }}</li></ul>
          <button class="btn btn-secondary" :data-testid="`kiro-query-${kind}`" :disabled="busy || uncertain || runtime.refreshState !== 'idle'" @click="run(kind)">{{ t(`${key}.${kind === 'models' ? 'queryModels' : 'queryQuota'}`) }}</button>
        </section>
        <form autocomplete="off" class="space-y-3" @submit.prevent="mutate('configure')">
          <p class="text-sm text-gray-600 dark:text-gray-300">{{ t(`${key}.identity`) }}</p>
          <label class="block">User-Agent<input v-model="userAgent" class="input mt-1" maxlength="512" :disabled="busy" /></label>
          <label class="block">x-amz-user-agent<input v-model="amzUserAgent" class="input mt-1" maxlength="512" :disabled="busy" /></label>
          <label class="block">{{ t(`${key}.machine`) }}<input v-model="machineID" class="input mt-1" maxlength="256" :disabled="busy" /></label>
          <button class="btn btn-secondary" data-testid="kiro-configure" :disabled="busy || uncertain || runtime.refreshState !== 'idle' || !(userAgent.trim() || amzUserAgent.trim() || machineID.trim())">{{ t(`${key}.configure`) }}</button>
        </form>
        <form autocomplete="off" class="space-y-3" @submit.prevent="mutate('reimport')">
          <h3 class="font-medium">{{ t(`${key}.reimport`) }}</h3>
          <label class="block" for="kiro-replacement-secret">{{ t(`${key}.secret`) }}</label>
          <textarea id="kiro-replacement-secret" ref="secretElement" v-model="secret" data-testid="kiro-reimport-secret" class="input font-mono" rows="5" autocomplete="off" autocapitalize="off" :spellcheck="false" data-private="true" data-1p-ignore data-lpignore="true" :disabled="busy" aria-describedby="kiro-replacement-help" />
          <p id="kiro-replacement-help" class="text-sm text-gray-600 dark:text-gray-300">{{ t(`${key}.secretHelp`) }}</p>
          <label class="flex gap-2"><input v-model="confirmed" data-testid="kiro-confirm" type="checkbox" :disabled="busy" />{{ t(`${key}.confirm`) }}</label>
          <button type="button" class="btn btn-secondary" @click="clearSecret">{{ t(`${key}.clear`) }}</button>
          <button class="btn btn-primary" data-testid="kiro-reimport" :disabled="busy || uncertain || !confirmed || !secret.trim() || runtime.refreshState === 'unknown'">{{ t(`${key}.submit`) }}</button>
        </form>
      </template>
      <button class="btn btn-secondary" @click="close">{{ t(`${key}.close`) }}</button>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { parseKiroImport, requestKiroRuntime, type KiroRuntime, type KiroRuntimeUpdate } from '@/api/admin/kiro'
const props = defineProps<{ show: boolean; accountId: number }>()
const emit = defineEmits<{ close: []; updated: [] }>()
const auth = useAuthStore()
const { t } = useI18n()
const key = 'admin.accounts.kiroManagement'
const unknown = computed(() => t(`${key}.unknown`))
const runtime = ref<KiroRuntime | null>(null)
const busy = ref(false), error = ref(false), saved = ref(false), uncertain = ref(false)
const userAgent = ref(''), amzUserAgent = ref(''), machineID = ref(''), secret = ref(''), confirmed = ref(false)
const secretElement = ref<HTMLTextAreaElement | null>(null)
let controller: AbortController | null = null
function clearSecret() { secret.value = ''; if (secretElement.value) secretElement.value.value = ''; confirmed.value = false }
function reset() {
  clearSecret(); controller?.abort(); controller = null; runtime.value = null
  busy.value = false; error.value = false; saved.value = false; uncertain.value = false
  userAgent.value = ''; amzUserAgent.value = ''; machineID.value = ''
}
function close() { reset(); emit('close') }
async function run(action: 'runtime' | 'models' | 'quota' = 'runtime', update?: KiroRuntimeUpdate) {
  if (busy.value || !auth.isAdmin || !props.show || !auth.token) { clearSecret(); return }
  clearSecret(); error.value = false; saved.value = false
  const active = new AbortController(); controller = active; busy.value = true
  try {
    const pending = requestKiroRuntime(props.accountId, auth.token, active.signal, action, update)
    if (update?.credential) update.credential = undefined // Serialized by the one-shot boundary; release parsed secrets now.
    const next = await pending
    if (controller !== active || !props.show || !auth.isAdmin) return
    runtime.value = next; uncertain.value = false
    if (update) { saved.value = true; userAgent.value = ''; amzUserAgent.value = ''; machineID.value = ''; emit('updated') }
  } catch {
    if (controller === active) { error.value = true; uncertain.value = true }
  } finally {
    if (update?.credential) { update.credential = undefined }
    if (controller === active) { busy.value = false; controller = null; clearSecret() }
  }
}
async function mutate(operation: KiroRuntimeUpdate['operation']) {
  if (busy.value || uncertain.value || !runtime.value) { clearSecret(); return }
  const current = runtime.value
  const update: KiroRuntimeUpdate = { operation, expected_version: current.version }
  if (operation === 'reimport') {
    if (!confirmed.value || current.refreshState === 'unknown' || (current.refreshState !== 'idle' && !current.refreshOwner)) { clearSecret(); error.value = true; return }
    try { update.credential = parseKiroImport(secret.value) } catch { clearSecret(); error.value = true; return }
    update.expected_refresh_owner = current.refreshOwner
  }
  if (operation === 'configure') {
    if (userAgent.value.trim()) update.user_agent = userAgent.value.trim()
    if (amzUserAgent.value.trim()) update.amz_user_agent = amzUserAgent.value.trim()
    if (machineID.value.trim()) update.machine_id = machineID.value.trim()
  }
  clearSecret()
  await run('runtime', update)
}
watch(() => [props.show, props.accountId, auth.isAdmin] as const, () => { reset(); if (props.show && auth.isAdmin) void run() }, { immediate: true })
onBeforeUnmount(reset)
</script>
