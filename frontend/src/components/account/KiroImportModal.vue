<template>
  <BaseDialog v-if="auth.isAdmin" :show="show" :title="copy.title" width="wide" @close="close">
    <form autocomplete="off" novalidate class="space-y-4" @submit.prevent="submit">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ copy.notice }}</p>
      <label class="block">{{ copy.name }}<input v-model="name" data-testid="kiro-name" class="input mt-1" maxlength="100" required :disabled="busy" /></label>
      <div class="grid gap-4 sm:grid-cols-2">
        <label>{{ copy.region }}<select v-model="region" data-testid="kiro-region" class="input mt-1" :disabled="busy"><option value="us-east-1">us-east-1</option><option value="us-west-2">us-west-2</option></select></label>
        <label>{{ copy.mode }}<select v-model="mode" data-testid="kiro-mode" class="input mt-1" :disabled="busy"><option value="social">Social</option><option value="idc">IdC / Builder ID</option></select></label>
      </div>
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ copy.identity }}</p>
      <label class="block">User-Agent<input v-model="userAgent" data-testid="kiro-user-agent" class="input mt-1" maxlength="512" required :disabled="busy" /></label>
      <label class="block">x-amz-user-agent<input v-model="amzUserAgent" data-testid="kiro-amz-user-agent" class="input mt-1" maxlength="512" required :disabled="busy" /></label>
      <label class="block">{{ copy.machine }}<input v-model="machineID" data-testid="kiro-machine-id" class="input mt-1" maxlength="256" :disabled="busy" /></label>
      <label class="block" for="kiro-secret-json">{{ copy.secret }}</label>
      <textarea id="kiro-secret-json" ref="secretElement" v-model="secret" data-testid="kiro-secret" class="input font-mono" rows="6" autocomplete="off" autocapitalize="off" :spellcheck="false" data-private="true" data-1p-ignore data-lpignore="true" :disabled="busy" aria-describedby="kiro-secret-help" />
      <p id="kiro-secret-help" class="text-sm text-gray-600 dark:text-gray-300">{{ copy.secretHelp }}</p>
      <p v-if="error" role="alert" data-testid="kiro-error" class="text-sm text-red-600">{{ error }}</p>
      <div v-if="result" role="status" data-testid="kiro-result" class="space-y-1 text-sm">
        <p>{{ copy.imported }} — {{ readinessLabel }}</p>
        <p>{{ copy.usage }}</p>
      </div>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="clearSecret">{{ copy.clear }}</button>
        <button type="button" class="btn btn-secondary" @click="close">{{ copy.close }}</button>
        <button type="submit" class="btn btn-primary" data-testid="kiro-submit" :disabled="busy || !secret.trim()">{{ busy ? copy.submitting : copy.submit }}</button>
      </div>
    </form>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { importKiroAccount, parseKiroImport, type KiroAuthMode, type KiroReadiness, type KiroRegion } from '@/api/admin/kiro'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; imported: [] }>()
const auth = useAuthStore()
const { locale } = useI18n()
const copy = computed(() => locale.value.startsWith('zh') ? {
  title: '导入 Kiro 原生 JSON', name: '账号名称', region: '受信任区域', mode: '认证模式',
  notice: '仅管理员显式导入单个 kiro.rs JSON 对象；不登录、不启动浏览器或设备授权，不读取本机凭据。导入不代表可推理或可收费。',
  identity: '填写已获授权客户端的真实稳定身份，不随机生成身份。区域与认证模式由此表单决定，不采信 JSON 中的端点、代理或路由配置。',
  machine: 'Machine ID（可选，真实客户端身份）', secret: '秘密 JSON（仅临时内存）',
  secretHelp: '不保存到浏览器存储、不记录或分析，不提供秘密导出/预览。提交尝试、关闭或卸载时清空。浏览器内存无法保证安全擦除。失败后请核对账号列表再重试，避免重复导入。',
  clear: '清空秘密', close: '关闭', submit: '显式导入', submitting: '正在导入…',
  invalid: 'JSON 格式无效或不受支持。请粘贴单个受支持的 kiro.rs 凭据对象。', failed: '导入未确认成功。请核对账号列表；凭据已清空。',
  imported: '已导入', ready: '凭据就绪（不保证上游可用）', refresh: '需要刷新凭据', blocked: '已阻断（不可用）', settlement: '已阻断：等待定价/结算接线', unknown: '凭据状态未知',
  usage: '用量未知；未确认供应商 token、额度或收费。不以零值代替未知，不保证服务可用。'
} : {
  title: 'Import native Kiro JSON', name: 'Account name', region: 'Trusted region', mode: 'Authentication mode',
  notice: 'Admin-only explicit import of one kiro.rs JSON object. No login, browser/device authorization, or host credential discovery. Import does not enable inference or charging.',
  identity: 'Supply the legitimate, stable identity of your authorized client; do not generate a random identity. Region and authentication mode come from this form, not JSON endpoint, proxy, or routing settings.',
  machine: 'Machine ID (optional, legitimate client identity)', secret: 'Secret JSON (transient memory only)',
  secretHelp: 'No browser persistence, logging, analytics, secret export, or preview. Cleared on every submission attempt, close, or unmount. Secure browser memory erasure cannot be guaranteed. Check the account list before retrying a failed import to avoid duplicates.',
  clear: 'Clear secret', close: 'Close', submit: 'Explicitly import', submitting: 'Importing…',
  invalid: 'Invalid or unsupported JSON. Paste one supported kiro.rs credential object.', failed: 'Import success was not confirmed. Check the account list; credentials were cleared.',
  imported: 'Imported', ready: 'Credentials ready (upstream availability not guaranteed)', refresh: 'Credential refresh required', blocked: 'Blocked (not usable)', settlement: 'Blocked: pricing/settlement integration required', unknown: 'Credential state unknown',
  usage: 'Usage unknown; provider tokens, quota, and charges are not confirmed. Unknown is not zero. Service availability is not guaranteed.'
})
const name = ref('')
const region = ref<KiroRegion>('us-west-2')
const mode = ref<KiroAuthMode>('social')
const userAgent = ref('')
const amzUserAgent = ref('')
const machineID = ref('')
const secret = ref('')
const secretElement = ref<HTMLTextAreaElement | null>(null)
const busy = ref(false)
const error = ref('')
const result = ref<KiroReadiness | null>(null)
let controller: AbortController | null = null
const readinessLabel = computed(() => result.value === 'ready' ? copy.value.ready : result.value === 'refresh_required' ? copy.value.refresh : result.value === 'settlement_required' ? copy.value.settlement : result.value === 'blocked' ? copy.value.blocked : copy.value.unknown)
function clearSecret() {
  secret.value = ''
  // Clear DOM synchronously, including before Vue's next render or detaching the modal.
  if (secretElement.value) secretElement.value.value = ''
}
function reset() {
  clearSecret()
  controller?.abort()
  controller = null
  busy.value = false
  error.value = ''
  result.value = null
  name.value = ''; userAgent.value = ''; amzUserAgent.value = ''; machineID.value = ''
  region.value = 'us-west-2'; mode.value = 'social'
}
function close() { reset(); emit('close') }
watch(() => props.show, show => { if (!show) reset() })
watch(() => auth.isAdmin, admin => { if (!admin) reset() })
onBeforeUnmount(reset)
async function submit() {
  if (busy.value || !auth.isAdmin || !props.show) return
  error.value = ''; result.value = null
  let credential: Record<string, unknown>
  try { credential = parseKiroImport(secret.value) } catch {
    clearSecret(); error.value = copy.value.invalid; return
  }
  clearSecret()
  if (!name.value.trim() || !userAgent.value.trim() || !amzUserAgent.value.trim() || !auth.token || !['us-east-1', 'us-west-2'].includes(region.value) || !['social', 'idc'].includes(mode.value)) {
    error.value = copy.value.failed; return
  }
  const active = new AbortController()
  controller = active
  busy.value = true
  try {
    const pending = importKiroAccount({ name: name.value.trim(), credential, region: region.value, auth_mode: mode.value, user_agent: userAgent.value.trim(), amz_user_agent: amzUserAgent.value.trim(), ...(machineID.value.trim() ? { machine_id: machineID.value.trim() } : {}) }, auth.token, active.signal)
    credential = {} // Release the component's parsed-object reference once serialized by the request boundary.
    const response = await pending
    if (controller !== active || !props.show || !auth.isAdmin) return
    result.value = response.readiness
    emit('imported')
  } catch {
    if (controller === active) error.value = copy.value.failed
  } finally {
    credential = {}
    if (controller === active) { busy.value = false; controller = null; clearSecret() }
  }
}
</script>
