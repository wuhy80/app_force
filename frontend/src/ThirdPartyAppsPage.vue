<script setup lang="ts">
const applications = [
  {
    id: 'cockpit-tools',
    name: 'Cockpit Tools',
    initials: 'CT',
    category: 'AI IDE 账号管理',
    description: '管理 Antigravity 等 AI IDE 的多个账号，查看模型剩余额度，支持一键切号和按额度自动切换。',
    platforms: 'Windows · macOS · Linux',
    license: 'CC BY-NC-SA 4.0（非商业使用）',
    downloadHint: 'Windows 可选择 MSI 安装包或便携 ZIP；其他系统请在发布页选择对应安装包。',
    links: [
      { label: '项目主页', url: 'https://github.com/jlcodes99/cockpit-tools' },
      { label: '下载版本', url: 'https://github.com/jlcodes99/cockpit-tools/releases/latest' },
    ],
  },
]

function openExternalLink(event: MouseEvent, url: string) {
  if (!window.runtime?.BrowserOpenURL) return
  event.preventDefault()
  window.runtime.BrowserOpenURL(url)
}
</script>

<template>
  <div class="third-party-page">
    <div class="page-title">
      <div>
        <span class="eyebrow">THIRD-PARTY APPS</span>
        <h1>第三方应用<span class="title-count">{{ applications.length }}</span></h1>
        <p>查看实用工具的项目与下载地址，选择适合你的应用。</p>
      </div>
    </div>

    <section class="third-party-list" aria-label="第三方应用列表">
      <article v-for="app in applications" :key="app.id" class="third-party-app" :aria-labelledby="`${app.id}-title`">
        <div class="third-party-heading">
          <span class="third-party-avatar" aria-hidden="true">{{ app.initials }}</span>
          <div class="third-party-name">
            <span>{{ app.category }}</span>
            <h2 :id="`${app.id}-title`">{{ app.name }}</h2>
          </div>
          <div class="third-party-actions">
            <a v-for="(link, index) in app.links" :key="link.url" :class="index === 0 ? 'secondary' : 'primary'" :href="link.url" target="_blank" rel="noopener noreferrer" @click="openExternalLink($event, link.url)">
              {{ link.label }}<span aria-hidden="true">↗</span>
            </a>
          </div>
        </div>
        <p class="third-party-description">{{ app.description }}</p>
        <dl class="third-party-meta">
          <div><dt>支持系统</dt><dd>{{ app.platforms }}</dd></div>
          <div><dt>许可</dt><dd>{{ app.license }}</dd></div>
        </dl>
        <p class="third-party-download-hint">{{ app.downloadHint }}</p>
        <div class="third-party-links">
          <div v-for="link in app.links" :key="link.url">
            <span>{{ link.label }}</span>
            <a :href="link.url" target="_blank" rel="noopener noreferrer" @click="openExternalLink($event, link.url)">{{ link.url }}</a>
          </div>
        </div>
      </article>
    </section>

    <div class="footnote"><span aria-hidden="true">ⓘ</span><p>以上应用由第三方提供和维护。下载入口会在浏览器中打开项目发布页，请选择对应系统的版本。</p></div>
  </div>
</template>

<style scoped>
.third-party-list{border-top:1px solid var(--line)}
.third-party-app{padding:26px 0;border-bottom:1px solid var(--line)}
.third-party-heading{display:flex;align-items:center;gap:15px}
.third-party-avatar{width:46px;height:46px;border:1px solid #e0e4ef;background:#f0f3fa;color:#677797;border-radius:10px;display:flex;align-items:center;justify-content:center;font-size:15px;font-weight:600;flex-shrink:0}
.third-party-name{flex:1;min-width:0}
.third-party-name>span{font-size:10px;color:var(--muted)}
.third-party-name h2{font-size:19px;margin-top:5px;line-height:1.4}
.third-party-actions{display:flex;gap:10px}
.third-party-actions a{text-decoration:none}
.third-party-page a:focus-visible{outline:3px solid #315cea45;outline-offset:3px}
.third-party-description{font-size:12px;color:#667185;line-height:1.9;margin-top:22px;max-width:680px}
.third-party-meta{display:flex;flex-wrap:wrap;gap:12px 30px;margin:18px 0 12px;font-size:11px;line-height:1.8}
.third-party-meta>div{display:flex;gap:12px}
.third-party-meta dt{color:var(--muted)}
.third-party-meta dd{margin:0;color:#667185}
.third-party-download-hint{font-size:11px;color:#828a98;line-height:1.8}
.third-party-links{display:flex;flex-direction:column;gap:10px;margin-top:22px;padding-top:17px;border-top:1px solid var(--line);font-size:11px}
.third-party-links>div{display:flex;gap:18px;align-items:baseline;line-height:1.8}
.third-party-links>div>span{color:var(--muted);flex-shrink:0}
.third-party-links a{color:var(--accent);text-decoration:none;overflow-wrap:anywhere;min-width:0}
.third-party-links a:hover{text-decoration:underline}
@media(max-width:760px){.third-party-heading{flex-wrap:wrap;gap:12px}.third-party-actions{width:100%;margin-top:6px}.third-party-actions a{flex:1}.third-party-name h2{font-size:17px}.third-party-description{margin-top:18px}.third-party-meta{flex-direction:column;gap:8px}.third-party-links>div{flex-direction:column;gap:3px}.third-party-app{padding:22px 0}}
</style>
