<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { APIError, desktop, request, setToken, type Device, type LogEntry, type Node, type Process, type Rule, type Settings, type State } from './api'

type Page = 'programs' | 'nodes' | 'connections' | 'devices' | 'settings' | 'logs'
const pages: { id: Page; label: string; icon: string }[] = [
  { id: 'programs', label: '程序代理', icon: '▦' }, { id: 'nodes', label: '订阅与节点', icon: '◈' },
  { id: 'connections', label: '实时连接', icon: '⇄' }, { id: 'devices', label: '设备管理', icon: '▣' },
  { id: 'settings', label: '代理设置', icon: '⚙' }, { id: 'logs', label: '运行日志', icon: '≡' },
]
const page = ref<Page>('programs'), state = ref<State>(), local = ref<State>(), selectedDevice = ref('')
const error = ref(''), notice = ref(''), warnings = ref<string[]>([]), busy = ref(false), loading = ref(true), unauthorized = ref(false)
const loginToken = ref(''), processSearch = ref(''), nodeSearch = ref(''), processes = ref<Process[]>([]), logs = ref<LogEntry[]>([])
const showProcessPicker = ref(false), showImport = ref(false), showDeviceForm = ref(false)
const manualPath = ref(''), manualName = ref(''), importName = ref(''), importURL = ref(''), importContent = ref(''), importMode = ref<'url' | 'text'>('url')
const settings = ref<Settings>(), deviceDraft = ref<Device>({ id: '', name: '', url: 'https://', token: '', certificate_sha256: '' })
const latencies = ref<Record<string, string>>({}), testingNode = ref('')
let poll: ReturnType<typeof setInterval> | undefined
const path = (suffix: string) => selectedDevice.value ? `/api/remote/${selectedDevice.value}/${suffix}` : `/api/${suffix}`
const currentNode = computed(() => state.value?.nodes.find(n => n.id === state.value?.selected_node_id))
const enabledRules = computed(() => state.value?.rules.filter(r => r.enabled).length || 0)
const filteredNodes = computed(() => state.value?.nodes.filter(n => `${n.name} ${n.type} ${n.server}`.toLowerCase().includes(nodeSearch.value.toLowerCase())) || [])
const filteredProcesses = computed(() => processes.value.filter(p => `${p.name} ${p.path}`.toLowerCase().includes(processSearch.value.toLowerCase())))
const activeConnections = computed(() => state.value?.engine.connections.filter(c => c.proxied).length || 0)
const deviceLabel = computed(() => local.value?.devices.find(d => d.id === selectedDevice.value)?.name || '本机')
const date = (value: string) => new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
const bytes = (value = 0) => value >= 1048576 ? `${(value / 1048576).toFixed(1)} MB` : value >= 1024 ? `${(value / 1024).toFixed(1)} KB` : `${value} B`
const ruleActivity = (rule: Rule) => state.value?.engine.connections.filter(c => c.proxied && c.process_path.toLowerCase() === rule.path.toLowerCase()).length || 0

function report(e: unknown) {
  error.value = e instanceof Error ? e.message : String(e)
  if (e instanceof APIError) { warnings.value = e.warnings; if (e.status === 401 && !selectedDevice.value) unauthorized.value = true }
}
async function refresh(initial = false) {
  const target = selectedDevice.value
  try {
    const localState = await request<State>('GET', '/api/state')
    if (target !== selectedDevice.value) return
    local.value = localState
    const next = target ? await request<State>('GET', `/api/remote/${target}/state`) : localState
    if (target !== selectedDevice.value) return
    state.value = next; unauthorized.value = false
    if (initial || !settings.value) settings.value = { ...next.settings }
  } catch (e) { report(e) } finally { loading.value = false }
}
async function action(work: () => Promise<void>, message = '') {
  if (busy.value) return
  busy.value = true; error.value = ''; notice.value = ''; warnings.value = []
  try { await work(); await refresh(); if (message) notice.value = message } catch (e) { report(e) } finally { busy.value = false }
}
async function switchDevice() {
  state.value = undefined; settings.value = undefined; processes.value = []; logs.value = []; error.value = ''; latencies.value = {}; loading.value = true
  if (page.value === 'devices') page.value = 'programs'
  await refresh(true)
}
async function choosePage(id: Page) {
  page.value = id
  if (id === 'settings' && state.value) settings.value = { ...state.value.settings }
  if (id === 'logs') await loadLogs()
}
async function login() { setToken(loginToken.value); error.value = ''; await refresh(true) }
async function toggleEngine() {
  await action(async () => { await request('POST', path(`engine/${state.value?.engine.running ? 'stop' : 'start'}`)) })
}
async function loadProcesses() {
  await action(async () => { processes.value = await request<Process[]>('GET', path('processes')) })
}
async function openPicker() { showProcessPicker.value = true; await loadProcesses() }
async function addProgram(p?: Process) {
  const rule = { name: p?.name || manualName.value, path: p?.path || manualPath.value, enabled: true }
  await action(async () => { await request('POST', path('rules'), rule); if (!p) { manualPath.value = ''; manualName.value = '' } }, '程序规则已保存，新连接将按规则处理')
}
async function chooseProgramFile() {
  try { const value = await desktop()?.ChooseProgram(); if (value) manualPath.value = value } catch (e) { report(e) }
}
async function toggleRule(rule: Rule) { await action(async () => { await request('POST', path('rules'), { ...rule, enabled: !rule.enabled }) }) }
async function deleteRule(rule: Rule) { await action(async () => { await request('DELETE', path(`rules/${rule.id}`)) }) }
async function selectNode(node: Node) { await action(async () => { await request('PUT', path('nodes/selected'), { id: node.id }) }, `已选择 ${node.name}`) }
async function testNode(node: Node) {
  if (testingNode.value) return
  testingNode.value = node.id
  try { const result = await request<{ latency_ms: number }>('POST', path(`nodes/${node.id}/probe`)); latencies.value[node.id] = `${result.latency_ms} ms` }
  catch (e) { latencies.value[node.id] = '不可用'; report(e) } finally { testingNode.value = '' }
}
async function importSubscription() {
  await action(async () => {
    const result = await request<{ count: number; warnings: string[] }>('POST', path('sources'), { name: importName.value, url: importMode.value === 'url' ? importURL.value : '', content: importMode.value === 'text' ? importContent.value : '' })
    warnings.value = result.warnings; showImport.value = false; importContent.value = ''; importURL.value = ''; importName.value = ''; notice.value = `已导入 ${result.count} 个节点`
  })
}
async function refreshSource(id: string) {
  await action(async () => { const result = await request<{ count: number; warnings: string[] }>('POST', path(`sources/${id}/refresh`)); warnings.value = result.warnings; notice.value = `订阅已更新，共 ${result.count} 个节点` })
}
async function deleteSource(id: string) {
  if (!confirm('删除该订阅及其全部节点？')) return
  await action(async () => { await request('DELETE', path(`sources/${id}`)) })
}
async function saveSettings() { await action(async () => { await request('PUT', path('settings'), settings.value) }, '代理设置已保存') }
async function saveDevice() {
  await action(async () => { await request('POST', '/api/devices', deviceDraft.value); showDeviceForm.value = false; deviceDraft.value = { id: '', name: '', url: 'https://', token: '', certificate_sha256: '' } }, '设备已添加，可从左上角切换')
}
async function deleteDevice(id: string) {
  if (!confirm('移除该设备的管理连接？')) return
  await action(async () => { await request('DELETE', `/api/devices/${id}`); if (selectedDevice.value === id) selectedDevice.value = '' })
}
async function loadLogs() { try { logs.value = await request<LogEntry[]>('GET', path('logs')) } catch (e) { report(e) } }
onMounted(async () => {
  await refresh(true)
  poll = setInterval(() => { if (!busy.value && !unauthorized.value) { void refresh(); if (page.value === 'logs') void loadLogs() } }, 3000)
})
onUnmounted(() => clearInterval(poll))
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <a class="brand" href="#" @click.prevent="choosePage('programs')"><span class="brand-mark">A<span>↗</span></span><span>AppForce<small>程序代理</small></span></a>
      <label class="device-select"><span class="eyebrow">当前设备</span><select v-model="selectedDevice" @change="switchDevice"><option value="">本机 · {{ local?.os || '连接中' }}</option><option v-for="d in local?.devices" :key="d.id" :value="d.id">{{ d.name }}</option></select><span class="device-meta">{{ state?.hostname || '等待连接' }}</span></label>
      <nav><button v-for="item in pages" :key="item.id" :class="{ active: page === item.id }" @click="choosePage(item.id)"><span class="nav-icon">{{ item.icon }}</span>{{ item.label }}<span v-if="item.id === 'programs' && enabledRules" class="nav-count">{{ enabledRules }}</span></button></nav>
      <div class="sidebar-bottom"><span class="engine-dot" :class="{ live: state?.engine.running }"></span>{{ state?.engine.running ? '内核运行中' : '内核已停止' }}<small>sing-box 内置 · v{{ state?.version || '0.1.0' }}</small></div>
    </aside>

    <main class="workspace">
      <header class="topbar"><span>{{ deviceLabel }} <span class="slash">/</span> {{ pages.find(p => p.id === page)?.label }}</span><select class="mobile-device-select" v-model="selectedDevice" aria-label="切换设备" @change="switchDevice"><option value="">本机</option><option v-for="d in local?.devices" :key="d.id" :value="d.id">{{ d.name }}</option></select><span class="platform-label">{{ state?.os || '—' }}<span class="engine-dot" :class="{ live: !!state }"></span></span></header>
      <div class="content">
        <div v-if="error" class="alert error" role="alert"><span>{{ error }}</span><button aria-label="关闭提示" @click="error = ''">×</button></div>
        <div v-if="notice" class="alert success" role="status"><span>{{ notice }}</span><button aria-label="关闭提示" @click="notice = ''">×</button></div>
        <details v-if="warnings.length" class="warning"><summary>{{ warnings.length }} 条导入提示</summary><p v-for="(w, i) in warnings" :key="i">{{ w }}</p></details>
        <div v-if="unauthorized" class="auth-panel"><div class="empty-symbol">⌁</div><h1>连接管理服务</h1><p>输入此设备的管理密钥。</p><form @submit.prevent="login"><input v-model="loginToken" type="password" placeholder="管理密钥" autocomplete="off" aria-label="管理密钥" required /><button class="primary" type="submit">连接</button></form><small>密钥保存在 AppForce 配置目录的 token 文件中。</small></div>
        <div v-else-if="loading" class="empty"><div class="spinner"></div><p>正在连接设备…</p></div>
        <div v-else-if="!state" class="empty"><h2>设备暂时不可用</h2><p>检查设备上的 AppForce 是否运行。</p><button class="secondary" @click="refresh(true)">重新连接</button></div>

        <template v-else>
          <template v-if="page === 'programs'">
            <div class="page-title"><div><span class="eyebrow">APPLICATION ROUTING</span><h1>程序代理<span class="title-count">{{ state.rules.length }}</span></h1><p>选择程序，让它的新连接通过当前节点转发。</p></div><button class="primary" :disabled="busy" @click="openPicker"><span>＋</span> 添加程序</button></div>
            <section class="engine-strip"><div class="engine-status"><span class="status-orbit" :class="{ running: state.engine.running }"><span></span></span><div><strong>{{ state.engine.running ? '代理已启动' : '准备就绪' }}</strong><p>{{ state.engine.running ? (state.engine.mode === 'tun' ? '所选程序代理 · 其他程序直连' : '本地 SOCKS / HTTP 代理') : '启动后应用已保存的程序规则' }}</p></div></div><div class="selected-node"><span class="eyebrow">当前节点</span><button @click="choosePage('nodes')">{{ currentNode?.name || '选择一个节点' }} <span>↗</span></button><small>{{ currentNode?.type.toUpperCase() || '尚未导入订阅' }}</small></div><button :class="state.engine.running ? 'secondary stop-button' : 'primary'" :disabled="busy || (!state.engine.running && !currentNode)" @click="toggleEngine">{{ busy ? '处理中…' : state.engine.running ? '停止代理' : '启动代理' }}</button></section>
            <div v-if="state.engine.error" class="inline-error">{{ state.engine.error }}</div>
            <div class="section-toolbar"><h2>已配置程序</h2><span>{{ enabledRules }} 个已启用 <span class="slash">·</span> {{ activeConnections }} 个代理连接</span></div>
            <div v-if="!state.rules.length" class="empty program-empty"><div class="empty-symbol">▦</div><h2>添加第一个程序</h2><p>从运行中的程序选择，或指定可执行文件路径。</p><button class="secondary" @click="openPicker">选择程序 <span>↗</span></button></div>
            <div v-else class="table-wrap"><table class="program-table"><thead><tr><th>程序</th><th>规则状态</th><th>代理连接</th><th class="align-right">操作</th></tr></thead><tbody><tr v-for="rule in state.rules" :key="rule.id"><td><div class="program-cell"><span class="app-avatar">{{ rule.name.slice(0, 1).toUpperCase() }}</span><div><strong>{{ rule.name }}</strong><small :title="rule.path">{{ rule.path }}</small></div></div></td><td><button class="toggle" :class="{ on: rule.enabled }" :disabled="busy" :aria-label="`${rule.enabled ? '停用' : '启用'} ${rule.name}`" :aria-pressed="rule.enabled" @click="toggleRule(rule)"><span></span></button><span class="muted rule-label">{{ rule.enabled ? '已启用' : '已停用' }}</span></td><td><span :class="{ 'live-text': ruleActivity(rule) > 0 }">{{ ruleActivity(rule) || '—' }}</span></td><td class="align-right"><button class="text-button muted" :disabled="busy" @click="deleteRule(rule)">移除</button></td></tr></tbody></table></div>
            <div class="footnote"><span>ⓘ</span><p>{{ state.settings.mode === 'tun' ? '规则按程序路径保存。浏览器等多进程应用可能需要添加辅助程序；已有连接通常需要重新建立。' : '当前为本地代理模式，程序规则不会生效。请切换到透明代理模式，或在应用中设置本地代理。' }}</p></div>
            <div class="traffic-footer"><div><span class="eyebrow">累计上传</span><strong>↑ {{ bytes(state.engine.upload) }}</strong></div><div><span class="eyebrow">累计下载</span><strong>↓ {{ bytes(state.engine.download) }}</strong></div><div><span class="eyebrow">本地代理</span><strong class="mono">127.0.0.1:{{ state.settings.mixed_port }}</strong></div><span class="muted">统计包含内核处理的直连流量</span></div>
          </template>

          <template v-if="page === 'nodes'">
            <div class="page-title"><div><span class="eyebrow">PROXY NODES</span><h1>订阅与节点</h1><p>导入订阅，选择所有已启用程序使用的节点。</p></div><button class="primary" :disabled="busy" @click="showImport = true">＋ 添加订阅</button></div>
            <section v-if="state.sources.length" class="sources"><div v-for="source in state.sources" :key="source.id" class="source-row"><span class="source-icon">◈</span><div class="source-name"><strong>{{ source.name }}</strong><small>{{ state.nodes.filter(n => n.source_id === source.id).length }} 个节点 · 更新于 {{ date(source.updated_at) }}<span v-if="source.warnings?.length"> · {{ source.warnings.length }} 条导入提示</span></small><details v-if="source.warnings?.length"><summary>查看导入提示</summary><p v-for="(w, i) in source.warnings" :key="i">{{ w }}</p></details></div><button v-if="source.url" class="text-button" :disabled="busy" @click="refreshSource(source.id)">更新</button><button class="text-button muted" :disabled="busy" @click="deleteSource(source.id)">删除</button></div></section>
            <div class="section-toolbar"><h2>节点 <span class="muted">{{ filteredNodes.length }}</span></h2><input v-model="nodeSearch" class="search-input" aria-label="搜索节点" placeholder="搜索节点、协议或服务器" /></div>
            <div v-if="!state.nodes.length" class="empty"><div class="empty-symbol">◈</div><h2>还没有代理节点</h2><p>支持 Clash YAML、sing-box JSON 和常见分享链接。</p><button class="secondary" @click="showImport = true">导入订阅</button></div>
            <div v-else class="node-list"><div v-for="node in filteredNodes" :key="node.id" class="node-row" :class="{ selected: node.id === state.selected_node_id }"><button class="node-select" :disabled="busy" :aria-label="`选择 ${node.name}`" @click="selectNode(node)"><span class="radio" :class="{ checked: node.id === state.selected_node_id }"></span><span class="node-info"><strong>{{ node.name }}</strong><small class="mono">{{ node.server }}:{{ node.port }}</small></span></button><span class="protocol">{{ node.type }}</span><span class="latency">{{ testingNode === node.id ? '测试中…' : latencies[node.id] || '未测试' }}</span><button class="text-button" :disabled="!!testingNode" @click="testNode(node)">测延迟</button></div></div>
            <div class="footnote"><span>ⓘ</span><p>延迟通过节点发起 HTTPS 请求测量。运行中切换节点会重新建立代理，现有连接可能中断。</p></div>
          </template>

          <template v-if="page === 'connections'">
            <div class="page-title"><div><span class="eyebrow">LIVE CONNECTIONS</span><h1>实时连接</h1><p>显示内核实际处理的连接，每 3 秒更新。</p></div><span class="live-indicator"><span class="engine-dot" :class="{ live: state.engine.running }"></span>{{ state.engine.running ? '实时更新' : '内核已停止' }}</span></div>
            <div v-if="!state.engine.connections.length" class="empty"><div class="empty-symbol">⇄</div><h2>暂无活动连接</h2><p>{{ state.engine.running ? '在所选程序中发起新的网络请求。' : '启动代理后，在这里查看实际连接。' }}</p></div>
            <div v-else class="table-wrap"><table><thead><tr><th>目标 / 程序</th><th>协议</th><th>转发方式</th><th>上传 / 下载</th></tr></thead><tbody><tr v-for="c in state.engine.connections" :key="c.id"><td><strong>{{ c.destination }}</strong><small class="path-text" :title="c.process_path">{{ c.process_path || '未识别进程' }}<span v-if="c.pid"> · PID {{ c.pid }}</span></small></td><td class="mono">{{ c.network.toUpperCase() }}</td><td><span class="route-label" :class="{ proxy: c.proxied }">{{ c.proxied ? '代理' : '直连' }}</span></td><td class="mono">↑ {{ bytes(c.upload) }} <span class="slash">/</span> ↓ {{ bytes(c.download) }}</td></tr></tbody></table></div>
          </template>

          <template v-if="page === 'devices'">
            <div class="page-title"><div><span class="eyebrow">CONNECTED DEVICES</span><h1>设备管理</h1><p>管理运行 AppForce 的 Windows、Linux 和 Mac。</p></div><button class="primary" @click="showDeviceForm = true">＋ 添加设备</button></div>
            <div class="device-row"><span class="app-avatar">{{ local?.os === 'darwin' ? 'M' : local?.os === 'windows' ? 'W' : 'L' }}</span><div><strong>{{ local?.hostname }}</strong><small>本机 · {{ local?.os }}</small></div><button class="text-button" @click="selectedDevice = ''; switchDevice()">管理 <span>↗</span></button></div>
            <div v-for="d in local?.devices" :key="d.id" class="device-row"><span class="app-avatar">↗</span><div><strong>{{ d.name }}</strong><small>{{ d.url }}</small></div><button class="text-button" @click="selectedDevice = d.id; switchDevice()">连接</button><button class="text-button muted" @click="deleteDevice(d.id)">移除</button></div>
            <div class="footnote"><span>ⓘ</span><p>每台设备独立运行代理。管理界面断开后，远端继续使用当前配置。远程服务需要 HTTPS，可填写证书指纹连接自签名证书。</p></div>
          </template>

          <template v-if="page === 'settings' && settings">
            <div class="page-title"><div><span class="eyebrow">ENGINE SETTINGS</span><h1>代理设置</h1><p>设置 {{ deviceLabel }} 的代理模式与 DNS 行为。</p></div><button class="primary" :disabled="busy" @click="saveSettings">保存设置</button></div>
            <div class="settings-list"><div class="setting-row"><div><h2>代理模式</h2><p>透明代理按程序转发；本地代理供应用自行配置。</p></div><select v-model="settings.mode"><option value="tun">透明代理 · TUN</option><option value="mixed">本地代理 · SOCKS / HTTP</option></select></div><div class="setting-row"><div><h2>本地代理端口</h2><p>仅监听 127.0.0.1。</p></div><input v-model.number="settings.mixed_port" type="number" min="1024" max="65535" aria-label="本地代理端口" /></div><div class="setting-row"><div><h2>DNS 查询方式</h2><p>透明代理模式下影响经过 TUN 的 DNS 查询，包括未选程序。</p></div><select v-model="settings.dns_mode"><option value="direct">加密 DNS · 直连</option><option value="proxy">加密 DNS · 通过当前节点</option></select></div><div class="setting-row"><div><h2>DNS 服务器</h2><p>填写支持 HTTPS /dns-query 的服务器 IP，如 1.1.1.1。</p></div><input v-model="settings.dns_address" aria-label="DNS 服务器" placeholder="1.1.1.1" /></div><div class="setting-row"><div><h2>严格路由</h2><p>加强路由约束，可能影响其他 VPN 或虚拟网卡。</p></div><input v-model="settings.strict_route" type="checkbox" aria-label="严格路由" /></div><div v-if="state.os === 'linux'" class="setting-row"><div><h2>Linux 路由优化</h2><p>使用 nftables 自动重定向，需要相应内核和权限支持。</p></div><input v-model="settings.auto_redirect" type="checkbox" aria-label="Linux 路由优化" /></div></div>
            <div class="footnote"><span>ⓘ</span><p>透明代理需要管理员或 root 权限。未识别进程默认直连；局域网和管理连接直连。节点失败时，匹配的程序不会自动回退直连。</p></div>
          </template>

          <template v-if="page === 'logs'">
            <div class="page-title"><div><span class="eyebrow">ENGINE LOG</span><h1>运行日志</h1><p>最近 250 条内核日志，仅保存在内存中。</p></div><button class="secondary" @click="loadLogs">刷新</button></div>
            <div v-if="!logs.length" class="empty"><div class="empty-symbol">≡</div><h2>暂无日志</h2><p>启动代理后显示内核运行信息。</p></div>
            <div v-else class="log-view"><div v-for="(entry, i) in logs" :key="i" class="log-line"><time>{{ new Date(entry.time).toLocaleTimeString('zh-CN') }}</time><span>{{ entry.level }}</span><code>{{ entry.message }}</code></div></div>
          </template>
        </template>
      </div>
    </main>

    <div v-if="showProcessPicker || showImport || showDeviceForm" class="modal-backdrop" @click.self="!busy && (showProcessPicker = showImport = showDeviceForm = false)">
      <section class="modal" :class="{ wide: showProcessPicker }" role="dialog" aria-modal="true" :aria-label="showProcessPicker ? '添加程序' : showImport ? '添加订阅' : '添加设备'">
        <header class="modal-header"><div><span class="eyebrow">{{ deviceLabel }}</span><h2>{{ showProcessPicker ? '添加程序' : showImport ? '添加订阅' : '添加设备' }}</h2></div><button class="close-button" :disabled="busy" aria-label="关闭" @click="showProcessPicker = showImport = showDeviceForm = false">×</button></header>
        <div v-if="error" class="alert error" role="alert">{{ error }}</div>
        <details v-if="warnings.length" class="warning"><summary>{{ warnings.length }} 条导入提示</summary><p v-for="(w, i) in warnings" :key="i">{{ w }}</p></details>
        <template v-if="showProcessPicker"><div class="picker-toolbar"><input v-model="processSearch" class="search-input" placeholder="搜索程序或路径" aria-label="搜索运行中的程序" /><button class="text-button" :disabled="busy" @click="loadProcesses">{{ busy ? '读取中…' : '刷新列表' }}</button></div><div class="process-picker"><div v-for="p in filteredProcesses" :key="p.pid" class="picker-row"><span class="app-avatar">{{ p.name.slice(0, 1).toUpperCase() }}</span><div><strong>{{ p.name }} <span class="muted mono">{{ p.pid }}</span></strong><small :title="p.path">{{ p.path || '无法读取路径，请检查权限' }}</small></div><button class="text-button" :disabled="busy || !p.selectable || !!state?.rules.some(r => r.path === p.path)" @click="addProgram(p)">{{ state?.rules.some(r => r.path === p.path) ? '已添加' : '添加' }}</button></div><p v-if="!filteredProcesses.length" class="empty-mini">{{ busy ? '正在读取程序…' : '没有匹配的程序，可在下方填写路径。' }}</p></div><form class="manual-program" @submit.prevent="addProgram()"><h3>指定程序路径</h3><div class="input-with-button"><input v-model="manualPath" placeholder="可执行文件的绝对路径" aria-label="程序绝对路径" required /><button v-if="desktop() && !selectedDevice" class="text-button" type="button" @click="chooseProgramFile">浏览</button></div><div class="form-inline"><input v-model="manualName" placeholder="显示名称（可选）" aria-label="程序显示名称" /><button class="primary" :disabled="busy || !manualPath" type="submit">添加规则</button></div></form></template>
        <form v-if="showImport" class="form" @submit.prevent="importSubscription"><label>订阅名称<input v-model="importName" placeholder="例如：我的订阅" /></label><div class="segmented"><button type="button" :class="{ selected: importMode === 'url' }" @click="importMode = 'url'">订阅地址</button><button type="button" :class="{ selected: importMode === 'text' }" @click="importMode = 'text'">粘贴内容</button></div><label v-if="importMode === 'url'">订阅地址<input v-model="importURL" type="url" placeholder="https://…" required autocomplete="off" /></label><label v-else>订阅或分享链接<textarea v-model="importContent" rows="8" placeholder="粘贴 Clash YAML、sing-box JSON、base64 或分享链接" required></textarea></label><p class="muted">导入后逐项校验节点；不支持的节点会列出原因。</p><button class="primary full-width" :disabled="busy" type="submit">{{ busy ? '正在导入并校验…' : '导入订阅' }}</button></form>
        <form v-if="showDeviceForm" class="form" @submit.prevent="saveDevice"><label>设备名称<input v-model="deviceDraft.name" placeholder="例如：开发服务器" required /></label><label>管理地址<input v-model="deviceDraft.url" type="url" placeholder="https://192.168.1.10:17891" required /></label><label>管理密钥<input v-model="deviceDraft.token" type="password" autocomplete="off" placeholder="远端配置目录中的 token" required /></label><label>证书 SHA-256 指纹（可选）<input v-model="deviceDraft.certificate_sha256" placeholder="自签名证书需要填写指纹" /></label><p class="muted">远端运行 AppForce --headless 并开启 HTTPS。SSH 隧道可使用回环 HTTP 地址。</p><button class="primary full-width" :disabled="busy" type="submit">保存设备</button></form>
      </section>
    </div>
  </div>
</template>
