<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { request, type TrafficReport } from './api'

const props = defineProps<{ endpoint: string; running: boolean }>()
const emit = defineEmits<{ error: [error: unknown] }>()
const report = ref<TrafficReport>()
const days = ref(7), appID = ref(''), search = ref(''), selectedDay = ref('')
const includeDirect = ref(localStorage.getItem('appforce-traffic-include-direct') === 'true')
const loading = ref(false), error = ref('')
let generation = 0, poll: ReturnType<typeof setInterval> | undefined
const periodLabel = computed(() => days.value === 1 ? '今天' : `近 ${days.value} 天`)
const selectedApp = computed(() => report.value?.applications.find(app => app.id === appID.value))
const applications = computed(() => report.value?.applications.filter(app => `${app.name} ${app.path}`.toLowerCase().includes(search.value.toLowerCase())) || [])
const maximum = computed(() => Math.max(1, ...report.value?.history.map(day => day.upload + day.download) || []))
const inspectedDay = computed(() => report.value?.history.find(day => day.date === selectedDay.value) || report.value?.history.at(-1))
const bytes = (value = 0) => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const index = value > 0 ? Math.max(0, Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)) : 0
  return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`
}
const timestamp = (value: string) => new Date(value).toLocaleString('zh-CN')
const tick = (date: string, index: number) => index === 0 || index === days.value - 1 || (days.value > 7 && index % Math.ceil(days.value / 6) === 0)

async function load(reset = false) {
  const current = ++generation
  loading.value = true; error.value = ''
  if (reset) report.value = undefined
  const query = new URLSearchParams({ days: String(days.value), include_direct: String(includeDirect.value) })
  if (appID.value) query.set('app', appID.value)
  try {
    const next = await request<TrafficReport>('GET', `${props.endpoint}?${query}`)
    if (current === generation) report.value = next
  } catch (e) {
    if (current === generation) { error.value = e instanceof Error ? e.message : String(e); emit('error', e) }
  } finally { if (current === generation) loading.value = false }
}
watch([days, appID], () => { selectedDay.value = ''; void load(true) })
watch(includeDirect, value => {
  localStorage.setItem('appforce-traffic-include-direct', String(value))
  if (appID.value) appID.value = ''
  else void load(true)
})
onMounted(() => { void load(); poll = setInterval(() => { if (!loading.value) void load() }, 3000) })
onUnmounted(() => { generation++; clearInterval(poll) })
</script>

<template>
  <div class="traffic-page">
    <div class="page-title"><div><span class="eyebrow">APPLICATION TRAFFIC</span><h1>流量统计</h1><p>按应用查看实时速率与历史用量。</p></div><span class="live-indicator"><span class="engine-dot" :class="{ live: running }"></span>{{ running ? '每 3 秒更新' : '代理已停止' }}</span></div>
    <div class="traffic-controls">
      <div class="segmented" aria-label="统计时段"><button v-for="n in [1, 7, 30, 90]" :key="n" :class="{ selected: days === n }" :aria-pressed="days === n" @click="days = n">{{ n === 1 ? '今天' : `近 ${n} 天` }}</button></div>
      <label class="connection-filter"><input v-model="includeDirect" type="checkbox" />包含内核处理的直连流量</label>
    </div>
    <div v-if="error" class="traffic-load-error" role="alert">{{ error }} <button class="text-button" @click="load()">重试</button></div>
    <div v-if="!report && loading" class="empty"><div class="spinner"></div><p>正在读取流量统计…</p></div>
    <template v-if="report">
      <div v-if="report.storage_error" class="warning" role="alert">{{ report.storage_error }}。当前显示内存中的统计。</div>
      <div class="traffic-context"><span>{{ selectedApp?.name || '全部应用' }}<span class="slash">/</span>{{ includeDirect ? '代理与直连' : '仅代理流量' }}</span><button v-if="appID" class="text-button" @click="appID = ''">查看全部应用 ×</button></div>
      <section class="traffic-metrics" aria-label="流量概览">
        <div><span>实时下载 / 上传</span><strong class="live-text">↓ {{ bytes(report.summary.download_rate) }}<small>/s</small></strong><p>↑ {{ bytes(report.summary.upload_rate) }}/s</p></div>
        <div><span>{{ periodLabel }}用量</span><strong>{{ bytes(report.summary.upload + report.summary.download) }}</strong><p>↓ {{ bytes(report.summary.download) }}<span>↑ {{ bytes(report.summary.upload) }}</span></p></div>
        <div><span>历史累计</span><strong>{{ bytes(report.summary.lifetime_upload + report.summary.lifetime_download) }}</strong><p>↓ {{ bytes(report.summary.lifetime_download) }}<span>↑ {{ bytes(report.summary.lifetime_upload) }}</span></p></div>
        <div><span>活跃连接</span><strong>{{ report.summary.active_connections }}<small>个</small></strong><p>{{ running ? '当前内核处理的连接' : '历史记录继续保留' }}</p></div>
      </section>
      <section class="traffic-history" aria-label="每日流量趋势">
        <div class="section-toolbar"><h2>每日流量</h2><div class="traffic-legend"><span><i></i>下载</span><span><i class="upload"></i>上传</span></div></div>
        <div class="traffic-chart-scale"><span>{{ bytes(maximum === 1 ? 0 : maximum) }}</span><span>{{ report.history[0]?.date }} — {{ report.history.at(-1)?.date }}</span></div>
        <div class="traffic-chart-scroll">
          <div class="traffic-chart" :style="{ minWidth: days > 30 ? '630px' : undefined }">
            <button v-for="(day, i) in report.history" :key="day.date" class="traffic-day" :class="{ inspected: inspectedDay?.date === day.date }" :aria-label="`${day.date}，下载 ${bytes(day.download)}，上传 ${bytes(day.upload)}`" :title="`${day.date}\n下载 ${bytes(day.download)}\n上传 ${bytes(day.upload)}`" :aria-pressed="inspectedDay?.date === day.date" @click="selectedDay = day.date">
              <span class="day-bars"><span class="bar-stack" :style="{ height: `${Math.max(1.3, (day.upload + day.download) / maximum * 100)}%` }"><span class="bar-upload" :style="{ height: `${day.upload / Math.max(1, day.upload + day.download) * 100}%` }"></span><span class="bar-download" :style="{ height: `${day.download / Math.max(1, day.upload + day.download) * 100}%` }"></span></span></span>
              <span class="day-label">{{ tick(day.date, i) ? day.date.slice(5).replace('-', '/') : ' ' }}</span>
            </button>
          </div>
        </div>
        <div class="traffic-day-detail"><span>{{ inspectedDay?.date }}</span><span>↓ 下载 {{ bytes(inspectedDay?.download) }}</span><span>↑ 上传 {{ bytes(inspectedDay?.upload) }}</span></div>
      </section>
      <section class="traffic-applications">
        <div class="section-toolbar"><h2>各应用流量 <span class="muted">{{ report.applications.length }}</span></h2><input v-model="search" class="search-input" type="search" placeholder="搜索应用或路径" aria-label="搜索统计应用" /></div>
        <div v-if="!applications.length" class="empty-mini">{{ search ? '没有匹配的应用' : '暂无流量记录。启动代理并使用应用后，将自动开始统计。' }}</div>
        <div v-else class="table-wrap"><table class="traffic-table"><thead><tr><th>应用</th><th class="align-right">实时下载 / 上传</th><th class="align-right">{{ periodLabel }}下载 / 上传</th><th class="align-right">历史下载 / 上传</th><th class="align-right">连接</th></tr></thead><tbody>
          <tr v-for="app in applications" :key="app.id" :class="{ selected: appID === app.id }"><td><div class="program-cell"><span class="app-avatar">{{ app.name.slice(0, 1).toUpperCase() }}</span><div><button class="traffic-app-name" :aria-label="`查看 ${app.name} 的流量历史`" :aria-pressed="appID === app.id" @click="appID = appID === app.id ? '' : app.id">{{ app.name }} <span>↗</span></button><small :title="app.path">{{ app.path.startsWith('unknown:') ? '连接来源无法归属到具体应用' : app.path }}</small></div></div></td>
            <td class="align-right traffic-numbers"><span :class="{ 'live-text': app.download_rate > 0 }">↓ {{ bytes(app.download_rate) }}/s</span><small>↑ {{ bytes(app.upload_rate) }}/s</small></td>
            <td class="align-right traffic-numbers"><span>↓ {{ bytes(app.download) }}</span><small>↑ {{ bytes(app.upload) }}</small></td>
            <td class="align-right traffic-numbers"><span>↓ {{ bytes(app.lifetime_download) }}</span><small>↑ {{ bytes(app.lifetime_upload) }}</small></td>
            <td class="align-right">{{ app.active_connections || '—' }}</td></tr>
        </tbody></table></div>
      </section>
      <div class="footnote traffic-explanation"><span>ⓘ</span><div><p>仅统计 AppForce 内核实际处理的流量。默认只含代理流量，未经过内核的连接不计入。点击应用可查看它的历史趋势。</p><p>历史从 {{ timestamp(report.collected_since) }} 开始记录，按此设备的本地日期汇总；退出应用、切换节点或重启后继续保留。Antigravity 的语言服务计入所属应用。</p><p>{{ report.saved_at ? `最近保存：${timestamp(report.saved_at)}` : '有新记录时自动保存' }} · 每 5 秒保存一次，正常停止时立即保存。</p></div></div>
    </template>
  </div>
</template>

<style scoped>
.traffic-controls{display:flex;align-items:center;justify-content:space-between;gap:20px;flex-wrap:wrap;margin-bottom:22px}.traffic-controls .segmented{gap:24px}.traffic-context{display:flex;align-items:center;justify-content:space-between;min-height:35px;font-size:12px;color:#67758d}.traffic-metrics{display:grid;grid-template-columns:1fr 1.2fr 1.2fr .8fr;gap:28px;border-top:1px solid var(--line);border-bottom:1px solid var(--line);padding:28px 0 30px;margin:8px 0 30px}.traffic-metrics>div>span{font-size:11px;color:#7d8797}.traffic-metrics strong{display:block;font-weight:550;font-size:24px;letter-spacing:-.6px;margin:13px 0 9px;font-variant-numeric:tabular-nums;white-space:nowrap}.traffic-metrics strong small{font-size:12px;color:#8692a6;font-weight:400;letter-spacing:0;margin-left:5px}.traffic-metrics p{font-size:10px;color:#7e899b;line-height:1.9;font-variant-numeric:tabular-nums}.traffic-metrics p span{display:inline-block;margin-left:12px}.traffic-history{margin-bottom:30px}.traffic-legend{display:flex;gap:20px;font-size:11px;color:#7c879a}.traffic-legend span{display:flex;align-items:center;gap:7px}.traffic-legend i{width:8px;height:8px;border-radius:2px;background:var(--accent)}.traffic-legend i.upload{background:#b8c9ff}.traffic-chart-scale{display:flex;justify-content:space-between;font-size:10px;color:#929daf;margin-bottom:14px}.traffic-chart-scroll{overflow-x:auto}.traffic-chart{display:flex;gap:4px;background:repeating-linear-gradient(to top,transparent 0,transparent 39px,#e7e9ee80 39px,#e7e9ee80 40px);background-size:100% 160px;background-repeat:no-repeat;border-bottom:1px solid var(--line)}.traffic-day{border:0;background:transparent;flex:1;min-width:0;padding:0;text-align:center;color:#97a1b1;border-radius:3px 3px 0 0}.traffic-day:hover,.traffic-day.inspected{background:#315cea08}.day-bars{display:flex;align-items:flex-end;justify-content:center;height:160px}.bar-stack{width:70%;max-width:48px;display:flex;flex-direction:column;overflow:hidden;border-radius:3px 3px 0 0;min-height:2px;background:#e1e7f4}.bar-upload{background:#b8c9ff}.bar-download{background:var(--accent)}.day-label{height:29px;display:block;font-size:9px;line-height:29px;white-space:nowrap}.traffic-day-detail{display:flex;gap:25px;align-items:center;margin-top:14px;font-size:11px;color:#7b879b;font-variant-numeric:tabular-nums;min-height:22px}.traffic-day-detail>span:first-child{color:#53637c}.traffic-table{min-width:760px}.traffic-table th{white-space:nowrap}.traffic-table td{font-variant-numeric:tabular-nums}.traffic-table tr.selected{background:#edf2ff80}.traffic-table .program-cell small{max-width:240px}.traffic-app-name{border:0;background:transparent;padding:0;text-align:left;font-size:12px;font-weight:550;color:#35425a}.traffic-app-name:hover,.traffic-app-name[aria-pressed=true]{color:var(--accent)}.traffic-app-name>span{margin-left:5px;font-size:10px;color:#8c9ab4}.traffic-numbers{white-space:nowrap;font-size:12px}.traffic-numbers small{display:block;margin-top:5px;font-size:10px;color:#8d98aa}.traffic-explanation p+p{margin-top:6px}.traffic-load-error{font-size:12px;color:#bc514b;margin-bottom:16px}.traffic-applications .section-toolbar h2 .muted{font-size:12px;font-weight:400;margin-left:6px}
@media(max-width:1100px){.traffic-metrics{grid-template-columns:1fr 1fr;gap:25px}.traffic-metrics strong{font-size:23px}.traffic-metrics p span{margin-left:10px}}
@media(max-width:760px){.traffic-controls{gap:12px}.traffic-controls .segmented{gap:20px}.traffic-controls .connection-filter{font-size:11px}.traffic-metrics{gap:22px 14px;padding:22px 0}.traffic-metrics strong{font-size:20px}.traffic-metrics p span{display:block;margin:0}.traffic-metrics>div>span{font-size:10px}.traffic-day-detail{gap:10px;flex-wrap:wrap;font-size:10px}.traffic-chart-scale{font-size:9px}.traffic-applications .section-toolbar{gap:10px}.traffic-applications .search-input{width:145px;font-size:10px}.traffic-applications h2{font-size:13px}.traffic-context{font-size:11px}.traffic-page .page-title{gap:8px}.traffic-page .live-indicator{white-space:nowrap;font-size:9px}}
</style>
