<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { request, type AIAccount, type AccountTargetStatus, type AccountOAuthSession, type TokenReport } from './api'

const props = defineProps<{ deviceId: string; deviceLabel: string; endpoint: string; os: string }>()
const emit = defineEmits<{ error: [error: unknown] }>()
const accounts = ref<AIAccount[]>([]), status = ref<AccountTargetStatus>(), usage = ref<TokenReport>()
const selection = ref(''), days = ref(7), busy = ref(false), loadingUsage = ref(false)
const message = ref(''), targetError = ref(''), showAdd = ref(false), showConfig = ref(false)
const userKey = `appforce-antigravity-user-${props.deviceId || 'local'}`
const savedClient = localStorage.getItem(`${userKey}-client`) || 'system'
const targetUser = ref(localStorage.getItem(userKey) || ''), client = ref(['system', 'legacy-ide', ...(props.os === 'linux' ? ['managed-cli'] : [])].includes(savedClient) ? savedClient : 'system')
const importName = ref(''), credential = ref(''), search = ref('')
const oauth = ref<AccountOAuthSession>(), oauthConfig = ref({ client_id: '', client_secret: '', cloud: 'daily', has_client_secret: false })
const upload = ref<HTMLInputElement>(), usageUpload = ref<HTMLInputElement>()
let alive = true, generation = 0, poll: ReturnType<typeof setInterval> | undefined, oauthPoll: ReturnType<typeof setInterval> | undefined
const selected = computed(() => accounts.value.find(a => a.id === selection.value))
const filtered = computed(() => accounts.value.filter(a => `${a.email} ${a.name}`.toLowerCase().includes(search.value.toLowerCase())))
const target = () => ({ user: targetUser.value.trim(), client: client.value })
const currentTarget = () => ({ device_id: props.deviceId, ...target() })
function rememberTarget() { localStorage.setItem(userKey, targetUser.value.trim()); localStorage.setItem(`${userKey}-client`, client.value) }
const period = computed(() => days.value === 1 ? '今天' : `近 ${days.value} 天`)
const maximum = computed(() => Math.max(1, ...usage.value?.history.map(b => b.total_tokens) || []))
const number = (value?: number) => value === undefined ? '—' : value.toLocaleString('zh-CN')
const date = (value?: string) => !value || value.startsWith('0001-') ? '未知' : new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
const accountName = (id: string, fallback = '') => accounts.value.find(a => a.id === id)?.email || (id === 'unassigned' ? '未归属' : fallback || '已移除账号')
const metric = (key: 'total_tokens' | 'input_tokens' | 'output_tokens' | 'cached_tokens' | 'thinking_tokens') => usage.value?.summary.requests ? number(usage.value.summary[key]) : '—'

function fail(error: unknown) { emit('error', error) }
async function loadAccounts() {
  const next = await request<AIAccount[]>('GET', '/api/antigravity/accounts')
  if (alive) { accounts.value = next; if (selection.value && selection.value !== 'unassigned' && !next.some(a => a.id === selection.value)) selection.value = '' }
}
async function loadUsage() {
  if (loadingUsage.value) return
  const current = ++generation
  loadingUsage.value = true
  const query = new URLSearchParams({ days: String(days.value), ...target() })
  if (selection.value) query.set('account', selection.value)
  try {
    const next = await request<TokenReport>('GET', `${props.endpoint}/usage?${query}`)
    if (alive && current === generation) { usage.value = next; targetError.value = '' }
  } catch (e) { if (alive && current === generation) targetError.value = e instanceof Error ? e.message : String(e) }
  finally { if (alive) { loadingUsage.value = false; if (current !== generation) void loadUsage() } }
}
async function loadStatus() {
  const next = await request<AccountTargetStatus>('GET', `${props.endpoint}/native/status?${new URLSearchParams(target())}`)
  if (alive) status.value = next
}
async function action(work: () => Promise<void>) {
  if (busy.value) return
  busy.value = true; message.value = ''
  try { await work() } catch (e) { if (alive) fail(e) } finally { if (alive) busy.value = false }
}
async function refreshTarget() {
  await action(async () => {
    rememberTarget()
    await loadStatus(); generation++; usage.value = undefined; await loadUsage()
  })
}
async function capture() {
  await action(async () => {
    const account = await request<AIAccount>('POST', '/api/antigravity/capture', currentTarget())
    if (!alive) return
    rememberTarget()
    await loadAccounts(); selection.value = account.id; await loadStatus(); message.value = `已保存 ${account.email}`
  })
}
async function importAccount() {
  await action(async () => {
    const account = await request<AIAccount>('POST', '/api/antigravity/accounts', { name: importName.value, credential: credential.value })
    credential.value = ''; importName.value = ''; showAdd.value = false
    if (!alive) return
    await loadAccounts(); selection.value = account.id; message.value = `已保存 ${account.email}`
  })
}
async function switchAccount(account: AIAccount) {
  await action(async () => {
    const next = await request<AccountTargetStatus>('POST', `/api/antigravity/accounts/${account.id}/switch`, currentTarget())
    if (alive) { rememberTarget(); status.value = next; message.value = next.message; await loadAccounts() }
  })
}
async function refreshQuota(account: AIAccount) {
  await action(async () => {
    try { await request('POST', `/api/antigravity/accounts/${account.id}/quota`) }
    finally { if (alive) await loadAccounts() }
  })
}
async function removeAccount(account: AIAccount) {
  if (!confirm(`移除已保存的 ${account.email}？历史用量会保留，目标客户端仍保持登录。`)) return
  await action(async () => { await request('DELETE', `/api/antigravity/accounts/${account.id}`); if (alive) await loadAccounts() })
}
async function saveConfig() {
  await action(async () => {
    const { client_id, client_secret, cloud } = oauthConfig.value
    await request('PUT', '/api/antigravity/config', { client_id, client_secret, cloud })
    oauthConfig.value.client_secret = ''; await loadConfig(); await loadAccounts(); message.value = '登录配置已保存'
  })
}
async function loadConfig() {
  const next = await request<typeof oauthConfig.value>('GET', '/api/antigravity/config')
  if (alive) oauthConfig.value = { ...next, client_secret: '' }
}
function openURL(url: string) { if (window.runtime?.BrowserOpenURL) window.runtime.BrowserOpenURL(url); else window.open(url, '_blank', 'noopener,noreferrer') }
async function startLogin() {
  await action(async () => {
    if (!oauthConfig.value.client_id) { showConfig.value = true; message.value = '请先保存 Google OAuth 桌面客户端配置，或导入已登录账号'; return }
    const next = await request<AccountOAuthSession>('POST', '/api/antigravity/oauth')
    if (!alive) return
    oauth.value = next; if (next.url) openURL(next.url)
    clearInterval(oauthPoll)
    let checking = false
    oauthPoll = setInterval(async () => {
      if (checking || !alive) return
      checking = true
      try {
        const result = await request<AccountOAuthSession>('GET', `/api/antigravity/oauth/${next.id}`)
        if (!alive) return
        oauth.value = { ...result, url: next.url }
        if (result.status !== 'waiting') {
          clearInterval(oauthPoll)
          if (result.status === 'succeeded') { await loadAccounts(); message.value = `已保存 ${result.email}` }
          else if (result.error) fail(new Error(result.error))
        }
      } catch (e) { clearInterval(oauthPoll); if (alive) fail(e) } finally { checking = false }
    }, 2000)
  })
}
async function readFile(event: Event, kind: 'account' | 'usage') {
  const input = event.target as HTMLInputElement, file = input.files?.[0]
  if (!file) return
  try {
    if (file.size > (kind === 'account' ? 65536 : 16 * 1048576)) throw new Error(kind === 'account' ? '账号文件不能超过 64 KB' : '用量文件不能超过 16 MB')
    const content = await file.text()
    if (!alive) return
    if (kind === 'account') credential.value = content
    else await action(async () => {
      const result = await request<{ count: number }>('POST', `${props.endpoint}/usage`, { ...target(), account_id: selection.value || 'unassigned', content })
      if (alive) { message.value = `已导入 ${result.count} 条新用量，重复记录已跳过`; await loadUsage() }
    })
  } catch (e) { fail(e) } finally { input.value = '' }
}
watch([days, selection], () => { generation++; usage.value = undefined; void loadUsage() })
watch(client, () => { status.value = undefined; generation++; usage.value = undefined; void loadUsage() })
watch(targetUser, () => { status.value = undefined; generation++; usage.value = undefined })
onMounted(async () => {
  try { await Promise.all([loadAccounts(), loadConfig(), loadStatus()]) } catch (e) { if (alive) fail(e) }
  if (!alive) return
  void loadUsage()
  poll = setInterval(() => { if (!busy.value && !loadingUsage.value) void loadUsage() }, 15000)
})
onUnmounted(() => { alive = false; generation++; clearInterval(poll); clearInterval(oauthPoll); credential.value = ''; oauthConfig.value.client_secret = '' })
</script>

<template>
  <div class="accounts-page">
    <div class="page-title"><div><span class="eyebrow">ANTIGRAVITY ACCOUNTS</span><h1>AI 账号与用量</h1><p>保存多个 Google 账号，切换 Antigravity / agy，并查看额度与 token 消耗。</p></div><button class="primary" :disabled="busy" @click="showAdd = true">＋ 添加账号</button></div>
    <div v-if="message" class="alert success" role="status">{{ message }}</div>
    <section class="account-target" aria-label="应用账号的设备和用户">
      <div><span class="eyebrow">应用到当前设备</span><h2>{{ deviceLabel }}</h2><p>{{ status?.email || status?.message || '读取账号状态…' }}</p></div>
      <form class="target-fields" @submit.prevent="refreshTarget"><label>运行用户<input v-model="targetUser" :placeholder="os === 'windows' ? '当前 Windows 用户' : '实际运行 agy / Antigravity 的用户名'" autocomplete="off" /></label><label>客户端<select v-model="client"><option value="system">Antigravity 2.x / agy</option><option v-if="os === 'linux'" value="managed-cli">无桌面 Linux · appforce-agy</option><option value="legacy-ide">旧版 Antigravity IDE</option></select></label><button class="secondary" :disabled="busy">读取状态</button></form>
    </section>
    <p class="target-note">{{ os === 'windows' ? '账号应用到当前 Windows 登录用户。' : '远程 Agent 的 root 身份与 CLI 用户不同，请填写实际使用的用户名。' }}{{ client === 'legacy-ide' ? '旧版 IDE 切号前请先关闭应用。' : client === 'managed-cli' ? '应用账号后，使用 ~/.local/bin/appforce-agy 启动 CLI。需要 dbus、libsecret-tools 和 gnome-keyring。' : '切号后请重启桌面客户端或新建 CLI 会话。' }}</p>
    <div class="section-toolbar"><h2>已保存账号 <span class="muted">{{ accounts.length }}</span></h2><div class="account-toolbar"><input v-model="search" class="search-input" placeholder="搜索邮箱或名称" aria-label="搜索账号" /><button class="text-button" :disabled="busy" @click="capture">导入当前登录</button><button class="text-button" :disabled="busy" @click="startLogin">Google 登录</button></div></div>
    <div v-if="oauth?.status === 'waiting'" class="warning" role="status">等待 Google 授权。<button class="text-button" @click="oauth.url && openURL(oauth.url)">重新打开登录页 ↗</button>请在运行 AppForce 管理端的电脑上完成登录。</div>
    <div v-if="!accounts.length" class="empty account-empty"><div class="empty-symbol">◎</div><h2>还没有保存账号</h2><p>先在 Antigravity 或 agy 登录，然后点击“导入当前登录”。更换账号后再次导入，即可保存多个账号。</p></div>
    <div v-else class="table-wrap"><table class="account-table"><thead><tr><th>账号</th><th>凭据有效期</th><th>额度更新时间</th><th>操作</th></tr></thead><tbody><tr v-for="account in filtered" :key="account.id" :class="{ selected: selection === account.id }"><td><button class="account-name" :aria-pressed="selection === account.id" @click="selection = account.id"><strong>{{ account.email }}</strong><small>{{ account.name !== account.email ? account.name : 'Google 个人账号' }}<span v-if="status?.account_id === account.id" class="active-badge">当前设备凭据</span></small></button></td><td>{{ date(account.expiry) }}<small>{{ account.refreshable ? '支持自动续期' : '到期后重新导入，或检查 OAuth 配置' }}</small></td><td>{{ account.quota ? date(account.quota.fetched_at) : '尚未查询' }}<small v-if="account.quota_error" class="quota-error">{{ account.quota_error }}</small></td><td class="account-actions"><button class="text-button" :disabled="busy" @click="switchAccount(account)">应用到 {{ deviceLabel }}</button><button class="text-button" :disabled="busy" @click="refreshQuota(account)">刷新额度</button><button class="text-button muted" :disabled="busy" @click="removeAccount(account)">移除</button></td></tr></tbody></table></div>
    <section v-if="selected" class="quota-section" aria-label="账号剩余额度"><div class="section-toolbar"><h2>{{ selected.email }} · 剩余额度</h2><span class="muted">{{ selected.quota?.tier || '' }}</span></div><p v-if="!selected.quota" class="muted">点击该账号的“刷新额度”获取模型额度和重置时间。</p><template v-else><div class="quota-buckets"><div v-for="bucket in selected.quota.buckets" :key="bucket.id" class="quota-bucket"><div><strong>{{ bucket.name }}</strong><span>{{ bucket.window }}</span></div><div class="quota-meter"><span :style="{ width: `${bucket.remaining ?? 0}%` }"></span></div><div><strong>{{ bucket.remaining === undefined ? '未知' : `${bucket.remaining.toFixed(1)}%` }}</strong><small>{{ bucket.reset_time ? `重置 ${date(bucket.reset_time)}` : '未提供重置时间' }}</small></div></div></div><p v-for="credit in selected.quota.credits" :key="credit.type" class="quota-credit">{{ credit.type }} · 剩余积分 {{ credit.amount }}</p><p class="target-note">更新时间 {{ date(selected.quota.fetched_at) }}。额度及积分按账号服务返回值展示，不换算为 token 数。</p></template></section>

    <section class="token-section" aria-label="Token 消耗统计">
      <div class="section-toolbar"><h2>Token 消耗 <span class="muted">{{ selected?.email || (selection === 'unassigned' ? '未归属' : '全部账号') }}</span></h2><button class="text-button" :disabled="loadingUsage || busy" @click="usageUpload?.click()">导入用量记录</button><input ref="usageUpload" hidden type="file" accept=".json,.jsonl,application/json" @change="readFile($event, 'usage')" /></div>
      <div class="token-controls"><div class="segmented" aria-label="用量统计时段"><button v-for="n in [1, 7, 30, 90]" :key="n" :class="{ selected: days === n }" @click="days = n">{{ n === 1 ? '今天' : `近 ${n} 天` }}</button></div><select v-model="selection" aria-label="筛选账号用量"><option value="">全部账号</option><option v-for="account in accounts" :key="account.id" :value="account.id">{{ account.email }}</option><option value="unassigned">未归属</option></select><button class="text-button" :disabled="loadingUsage" @click="loadUsage">{{ loadingUsage ? '读取中…' : '刷新' }}</button></div>
      <p v-if="targetError" class="inline-error" role="alert">{{ targetError }}</p>
      <div class="token-metrics"><div><span>{{ period }}报告总 token</span><strong>{{ metric('total_tokens') }}</strong><p>{{ usage?.summary.requests ? `${number(usage.summary.requests)} 次响应` : '暂无用量记录' }}</p></div><div><span>输入 token</span><strong>{{ metric('input_tokens') }}</strong><p>输出 {{ metric('output_tokens') }}</p></div><div><span>缓存读取 token</span><strong>{{ metric('cached_tokens') }}</strong><p>思考 {{ metric('thinking_tokens') }}</p></div><div><span>历史累计报告总 token</span><strong>{{ usage?.lifetime.requests ? number(usage.lifetime.total_tokens) : '—' }}</strong><p>{{ usage?.lifetime.requests ? `${number(usage.lifetime.requests)} 次响应` : '历史记录会持续保留' }}</p></div></div>
      <template v-if="usage?.summary.requests">
        <div class="token-chart" :style="{ minHeight: '160px' }" :aria-label="days === 1 ? '每小时 token 消耗' : '每日 token 消耗'"><div v-for="(bucket, i) in usage.history" :key="bucket.time" class="token-bar" :title="`${bucket.time}：${number(bucket.total_tokens)} token，${number(bucket.requests)} 次响应`"><div><span :style="{ height: `${Math.max(1, bucket.total_tokens / maximum * 100)}%` }"></span></div><small>{{ days === 1 ? bucket.time.slice(11) : usage.history.length <= 7 || i === 0 || i === usage.history.length - 1 ? bucket.time.slice(5) : '' }}</small></div></div>
        <div class="table-wrap"><table class="token-table"><thead><tr><th>模型</th><th>响应次数</th><th>输入</th><th>输出</th><th>缓存读取</th><th>思考</th><th>报告总数</th></tr></thead><tbody><tr v-for="model in usage.models" :key="model.id"><td>{{ model.name }}</td><td>{{ number(model.requests) }}</td><td>{{ number(model.input_tokens) }}</td><td>{{ number(model.output_tokens) }}</td><td>{{ number(model.cached_tokens) }}</td><td>{{ number(model.thinking_tokens) }}</td><td>{{ number(model.total_tokens) }}</td></tr></tbody></table></div>
        <div v-if="!selection" class="table-wrap"><table class="token-table"><thead><tr><th>账号</th><th>响应次数</th><th>输入</th><th>输出</th><th>报告总数</th></tr></thead><tbody><tr v-for="account in usage.accounts" :key="account.id"><td>{{ accountName(account.id, account.name) }}</td><td>{{ number(account.requests) }}</td><td>{{ number(account.input_tokens) }}</td><td>{{ number(account.output_tokens) }}</td><td>{{ number(account.total_tokens) }}</td></tr></tbody></table></div>
      </template>
      <div v-else-if="usage && !loadingUsage" class="empty-mini token-empty">{{ usage.files ? '当前时段没有可识别的 token 记录。' : '尚未找到 Antigravity / agy 会话日志。使用客户端后会自动采集，也可导入 JSON / JSONL 用量记录。' }}</div>
      <p v-for="warning in usage?.warnings" :key="warning" class="quota-error">{{ warning }}</p>
      <div class="footnote"><span>ⓘ</span><div><p>每 15 秒读取当前设备、所选用户的会话日志，仅保存 token 统计。总数遵循客户端报告；缓存、思考可能已包含在其他计数中，不重复相加。这里的历史覆盖已采集记录，不代表完整官方账单。</p><p>日志没有账号信息时，按 AppForce 记录的切号时段归属；开始管理前的旧记录显示为“未归属”。在客户端自行更换账号后，请重新导入当前登录。导入用量时，请先选择该文件实际所属账号。</p></div></div>
    </section>

    <details :open="showConfig" class="account-config" @toggle="showConfig = ($event.target as HTMLDetailsElement).open"><summary>配置 Google 登录与自动续期</summary><form class="form" @submit.prevent="saveConfig"><p class="muted">默认使用 Antigravity 公开的桌面 OAuth 配置，通常无需修改。自定义客户端仅用于你自己的登录流程；自动续期必须保留原登录使用的客户端。清空 Client ID 并保存可恢复默认配置。</p><label>Client ID<input v-model="oauthConfig.client_id" placeholder="…apps.googleusercontent.com" autocomplete="off" /></label><label>Client Secret<input v-model="oauthConfig.client_secret" type="password" :placeholder="oauthConfig.has_client_secret ? '已保存，留空保留' : '按 OAuth 客户端配置填写'" autocomplete="new-password" /></label><label>额度服务<select v-model="oauthConfig.cloud"><option value="daily">个人账号 · Daily</option><option value="production">Google Cloud · Production</option></select></label><button class="secondary" :disabled="busy">保存登录配置</button></form></details>
    <div v-if="showAdd" class="modal-backdrop" @click.self="!busy && (showAdd = false)"><section class="modal" role="dialog" aria-modal="true" aria-label="添加 Antigravity 账号"><header class="modal-header"><h2>导入账号</h2><button class="close-button" :disabled="busy" aria-label="关闭" @click="showAdd = false; credential = ''">×</button></header><form class="form" @submit.prevent="importAccount"><p class="muted">导入当前 Antigravity 个人账号凭据 JSON，保存前会向 Google 验证账号身份。也可关闭此窗口使用“导入当前登录”。</p><label>名称（可选）<input v-model="importName" placeholder="例如：工作账号" /></label><label>账号凭据<textarea v-model="credential" rows="8" autocomplete="off" spellcheck="false" placeholder='{"token":{"access_token":"…","refresh_token":"…","expiry":"…"},"auth_method":"consumer"}' required></textarea></label><input ref="upload" hidden type="file" accept=".json,application/json" @change="readFile($event, 'account')" /><button class="secondary" type="button" :disabled="busy" @click="upload?.click()">选择 JSON 文件</button><button class="primary" :disabled="busy || !credential.trim()">{{ busy ? '验证并保存中…' : '验证并保存账号' }}</button></form></section></div>
  </div>
</template>

<style scoped>
.account-target{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:22px 0;border-top:1px solid var(--line);border-bottom:1px solid var(--line)}.account-target h2{margin:6px 0;font-size:17px}.account-target p{font-size:11px;color:var(--muted);max-width:350px;line-height:1.7}.target-fields{display:flex;align-items:flex-end;gap:10px}.target-fields label{display:grid;gap:7px;font-size:10px;color:var(--muted)}.target-fields input{width:190px}.target-fields select{max-width:210px}.target-note{font-size:11px;color:var(--muted);line-height:1.8;margin:12px 0 24px}.account-toolbar{display:flex;align-items:center;gap:14px}.account-empty{padding:35px 0}.account-empty p{max-width:500px}.account-table{min-width:760px}.account-table td small{display:block;color:var(--muted);font-size:10px;line-height:1.7;margin-top:5px;max-width:260px}.account-name{border:0;background:transparent;text-align:left;padding:0;color:inherit}.account-name strong{font-size:12px}.account-name[aria-pressed=true] strong{color:var(--accent)}.account-table tr.selected{background:#315cea06}.active-badge{display:inline-block;margin-left:7px;color:var(--accent)}.account-actions .text-button{font-size:11px;margin-right:12px}.quota-error{font-size:11px;color:#bc514b!important;line-height:1.8}.quota-section{margin-top:28px}.quota-buckets{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px 30px}.quota-bucket{padding:10px 0}.quota-bucket>div{display:flex;align-items:center;justify-content:space-between;gap:14px}.quota-bucket strong{font-size:12px;font-weight:550}.quota-bucket span,.quota-bucket small{font-size:10px;color:var(--muted)}.quota-meter{height:5px;background:#e5eaf5;margin:12px 0;border-radius:3px;overflow:hidden}.quota-meter>span{height:100%;display:block;background:var(--accent);border-radius:3px}.quota-credit{font-size:12px;margin-top:14px}.token-section{margin-top:35px;padding-top:25px;border-top:1px solid var(--line)}.token-section h2 .muted{font-weight:400;font-size:11px;margin-left:8px}.token-controls{display:flex;align-items:center;gap:15px;flex-wrap:wrap}.token-controls select{max-width:260px}.token-metrics{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:22px;padding:28px 0}.token-metrics>div>span{font-size:10px;color:var(--muted)}.token-metrics strong{display:block;font-size:25px;font-weight:550;letter-spacing:-.5px;font-variant-numeric:tabular-nums;margin:12px 0}.token-metrics p{font-size:10px;line-height:1.8;color:var(--muted)}.token-chart{display:flex;gap:4px;margin:15px 0 25px;border-bottom:1px solid var(--line);overflow-x:auto}.token-bar{flex:1;min-width:16px;text-align:center}.token-bar>div{height:135px;display:flex;align-items:flex-end;justify-content:center;background:linear-gradient(to top,#e6e9f077 1px,transparent 1px);background-size:100% 45px}.token-bar>div>span{display:block;width:65%;max-width:40px;background:var(--accent);border-radius:3px 3px 0 0}.token-bar small{font-size:9px;line-height:25px;white-space:nowrap;color:var(--muted)}.token-table{min-width:630px;font-size:11px;font-variant-numeric:tabular-nums}.token-table td:not(:first-child){white-space:nowrap}.token-empty{padding:20px 0;font-size:12px}.account-config{margin:26px 0;border-top:1px solid var(--line);padding-top:20px}.account-config summary{font-size:12px;color:var(--muted);cursor:pointer}.account-config .form{max-width:600px;margin-top:20px}.footnote p+p{margin-top:8px}.form .muted{line-height:1.8;font-size:11px}
@media(max-width:1100px){.account-target{align-items:flex-start;flex-direction:column}.target-fields{width:100%;flex-wrap:wrap}.token-metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.account-toolbar{flex-wrap:wrap;justify-content:flex-end}.account-toolbar .search-input{width:150px}}
@media(max-width:760px){.quota-buckets{grid-template-columns:1fr}.target-fields input{width:150px}.target-fields select{width:175px}.account-toolbar{gap:10px}.account-toolbar .search-input{width:125px}.token-metrics strong{font-size:21px}.token-controls{gap:10px}.token-section h2 .muted{display:block;margin:7px 0}.account-actions .text-button{display:block;margin:5px 0}}
</style>
