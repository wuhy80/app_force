<script setup lang="ts">
import { onMounted, onUnmounted, reactive, ref } from 'vue'
import { APIError, request, type DeploymentCheck, type DeploymentJob, type DeploymentRequest } from './api'

const emit = defineEmits<{ running: [running: boolean]; completed: [job: DeploymentJob] }>()
const draft = reactive<DeploymentRequest>({ name: '', host: '', port: 22, username: '', password: '', sudo_password: '', management_port: 17891 })
const running = ref(false), error = ref(''), stage = ref(''), check = ref<DeploymentCheck>(), job = ref<DeploymentJob>()
const platforms = ref<{os: string; arch: string}[]>([]), loadingPlatforms = ref(true)
let poll: ReturnType<typeof setTimeout> | undefined, mounted = true
const clearPasswords = () => { draft.password = ''; draft.sudo_password = '' }
function setRunning(value: boolean) { running.value = value; emit('running', value) }

async function track(id: string) {
  try {
    const next = await request<DeploymentJob>('GET', `/api/deployment/jobs/${id}`)
    if (!mounted) return
    job.value = next; stage.value = next.stage; error.value = ''
    if (next.status === 'running') { poll = setTimeout(() => void track(id), 1500); return }
    sessionStorage.removeItem('appforce-deployment-job'); setRunning(false)
    if (next.status === 'failed') error.value = next.error
    else emit('completed', next)
  } catch (e) {
    if (!mounted) return
    if (e instanceof APIError && (e.status === 404 || e.status === 401)) {
      sessionStorage.removeItem('appforce-deployment-job'); setRunning(false)
      error.value = e.status === 404 ? '部署进度已失效。请检查设备列表，或重新输入密码部署。' : e.message
      return
    }
    error.value = `读取部署进度失败：${e instanceof Error ? e.message : String(e)}。正在重试。`
    poll = setTimeout(() => void track(id), 3000)
  }
}

async function deploy() {
  if (running.value) return
  error.value = ''; check.value = undefined; job.value = undefined; setRunning(true)
  stage.value = '连接 SSH 并识别设备'
  try {
    const next = await request<DeploymentCheck>('POST', '/api/deployment/check', { ...draft })
    if (!mounted) return
    check.value = next
    if (!next.bundled) throw new Error('当前安装包没有包含该设备的 Agent，请使用完整构建。')
    const started = await request<DeploymentJob>('POST', '/api/deployment/jobs', { ...draft, host_fingerprint: next.host_fingerprint })
    clearPasswords()
    if (!mounted) return
    job.value = started; stage.value = started.stage
    sessionStorage.setItem('appforce-deployment-job', started.id)
    void track(started.id)
  } catch (e) {
    clearPasswords()
    if (mounted) { error.value = e instanceof Error ? e.message : String(e); setRunning(false) }
  }
}

onMounted(async () => {
  const pending = sessionStorage.getItem('appforce-deployment-job')
  if (pending) { stage.value = '读取部署进度'; setRunning(true); void track(pending) }
  try { platforms.value = await request('GET', '/api/deployment/platforms') }
  catch (e) { error.value = e instanceof Error ? e.message : String(e) }
  finally { loadingPlatforms.value = false }
})
onUnmounted(() => { mounted = false; clearTimeout(poll); clearPasswords() })
</script>

<template>
  <form class="form deployment-form" @submit.prevent="deploy">
    <p class="deployment-intro">填写远端登录信息，自动安装并启动 Linux 或 Mac 上的 Agent。</p>
    <div v-if="error" class="alert error" role="alert">{{ error }}</div>
    <div v-if="!loadingPlatforms && !platforms.length" class="warning">当前构建未包含远程 Agent，需使用完整安装包。</div>
    <fieldset :disabled="running">
      <label>设备名称<input v-model="draft.name" placeholder="例如：Linux 开发机" required maxlength="200" /></label>
      <div class="deployment-fields"><label>IP 或主机名<input v-model="draft.host" placeholder="192.168.1.10" required autocomplete="off" /></label><label class="port-field">SSH 端口<input v-model.number="draft.port" type="number" min="1" max="65535" required /></label></div>
      <label>SSH 用户名<input v-model="draft.username" placeholder="例如：ubuntu 或 Mac 登录用户名" required autocomplete="off" /></label>
      <label>SSH 密码<input v-model="draft.password" type="password" placeholder="远端账号的登录密码" required autocomplete="new-password" /></label>
      <details class="deployment-advanced"><summary>高级设置</summary><label>sudo 密码（可选）<input v-model="draft.sudo_password" type="password" placeholder="留空时使用 SSH 密码" autocomplete="new-password" /></label><label>Agent 管理端口<input v-model.number="draft.management_port" type="number" min="1024" max="65535" required /></label></details>
    </fieldset>
    <div v-if="running || job" class="deployment-progress" role="status" aria-live="polite">
      <div><span class="engine-dot" :class="{ live: running || job?.status === 'succeeded' }"></span><strong>{{ stage }}</strong><span v-if="job">{{ job.progress }}%</span></div>
      <progress :value="job?.progress || 3" max="100" :aria-label="stage"></progress>
      <small v-if="job">{{ job.name }} · {{ job.host }}</small>
      <small v-if="check">{{ check.hostname }} · {{ check.os === 'darwin' ? 'macOS' : 'Linux' }} / {{ check.arch }}</small>
      <small v-if="check" class="deployment-fingerprint" :title="check.host_fingerprint">SSH 指纹：{{ check.host_fingerprint }}</small>
    </div>
    <p class="muted">远端需开启 SSH，账号需有 root 或 sudo 权限。Linux 使用 systemd，Mac 使用远程登录。密码仅用于本次部署，不保存。</p>
    <p class="muted">安装后 Agent 开机运行，通过 HTTPS 管理。防火墙需允许管理端口；成功后可选择代理节点和程序。</p>
    <button class="primary full-width" :disabled="running || loadingPlatforms || !platforms.length" type="submit">{{ running ? '正在部署…' : job?.status === 'failed' ? '重新部署并启动' : '部署并启动' }}</button>
  </form>
</template>

<style scoped>
.deployment-form fieldset{border:0;padding:0;margin:0;display:flex;flex-direction:column;gap:18px;min-width:0}.deployment-intro{font-size:12px;line-height:1.8;color:#7c879a}.deployment-fields{display:flex;gap:12px}.deployment-fields>label{flex:1;min-width:0}.deployment-fields>.port-field{flex:0 0 100px}.deployment-advanced{font-size:12px;color:#7d8797}.deployment-advanced summary{padding:2px 0}.deployment-advanced label{margin-top:16px}.deployment-progress{border-top:1px solid var(--line);border-bottom:1px solid var(--line);padding:18px 0;display:flex;flex-direction:column;gap:10px}.deployment-progress>div{display:flex;align-items:center;font-size:12px}.deployment-progress strong{font-weight:500;flex:1}.deployment-progress>div>span:last-child{color:#7c879a;font-size:11px}.deployment-progress progress{width:100%;height:5px;accent-color:var(--accent);border:0;border-radius:4px}.deployment-progress small{font-size:10px;color:#8b96a9;line-height:1.7}.deployment-fingerprint{overflow-wrap:anywhere;font-family:ui-monospace,Consolas,monospace}.deployment-form>.muted{font-size:11px;line-height:1.8}
</style>
