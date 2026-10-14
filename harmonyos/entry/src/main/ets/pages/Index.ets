import { bundleManager, common } from '@kit.AbilityKit';
import { DNSSettings, LiveConnection, LogEntry, MobileState, NativeReply, ProxyApplication,
  ProxyNode, Subscription, emptyState, errorMessage, formatBytes } from '../model/Types';
import { NativeClient } from '../service/NativeClient';
import { downloadSubscription } from '../service/SubscriptionDownload';
import { VpnController } from '../service/VpnController';

const BLUE: string = '#315CEA';
const INK: string = '#172033';
const MUTED: string = '#68758A';
const LINE: string = '#E9EDF3';

@Entry
@Component
struct Index {
  @State private data: MobileState = emptyState();
  @State private ready: boolean = false;
  @State private busy: boolean = false;
  @State private busyLabel: string = '';
  @State private message: string = '';
  @State private isError: boolean = false;
  @State private tab: number = 0;
  @State private importOpen: boolean = false;
  @State private importRemote: boolean = true;
  @State private sourceName: string = '';
  @State private sourceURL: string = '';
  @State private sourceContent: string = '';
  @State private nodeSearch: string = '';
  @State private appFormOpen: boolean = false;
  @State private appName: string = '';
  @State private appBundle: string = '';
  @State private dnsOpen: boolean = false;
  @State private bootstrapDNS: string = '223.5.5.5';
  @State private remoteDNS: string = '1.1.1.1';
  @State private activityLogs: boolean = false;
  private client?: NativeClient;
  private controller?: VpnController;
  private timer: number = -1;
  private polling: boolean = false;
  private visible: boolean = true;
  private stateEpoch: number = 0;

  aboutToAppear(): void { this.visible = true; this.initialize(); this.startPolling(); }
  onPageShow(): void { this.visible = true; this.startPolling(); }
  onPageHide(): void { this.visible = false; this.stopPolling(); }
  aboutToDisappear(): void { this.visible = false; this.stopPolling(); }

  private startPolling(): void {
    if (this.timer < 0) { this.timer = setInterval(() => { this.poll(); }, 2000); }
  }

  private stopPolling(): void {
    if (this.timer >= 0) { clearInterval(this.timer); this.timer = -1; }
  }

  private async initialize(): Promise<void> {
    await this.perform('读取配置', async () => {
      const context = this.getUIContext().getHostContext() as common.UIAbilityContext;
      const info = await bundleManager.getBundleInfoForSelf(bundleManager.BundleFlag.GET_BUNDLE_INFO_DEFAULT);
      const client = new NativeClient(context.filesDir);
      this.client = client;
      this.controller = new VpnController(client, info.name);
      this.data = await client.initialize(info.name);
      this.bootstrapDNS = this.data.settings.bootstrap_dns;
      this.remoteDNS = this.data.settings.remote_dns;
      this.ready = true;
    });
  }

  private requireClient(): NativeClient {
    const client = this.client;
    if (!client || !this.ready) { throw new Error('应用尚未就绪，请重试'); }
    return client;
  }

  private async poll(): Promise<void> {
    const client = this.client;
    if (!client || !this.ready || this.busy || this.polling || !this.visible) { return; }
    this.polling = true;
    const epoch = this.stateEpoch;
    try {
      const state = await client.state();
      if (!this.busy && epoch === this.stateEpoch) { this.data = state; }
    }
    catch (error) { this.message = errorMessage(error as Error); this.isError = true; }
    finally { this.polling = false; }
  }

  private async perform(label: string, action: () => Promise<void>): Promise<void> {
    if (this.busy) { return; }
    this.stateEpoch++;
    this.busy = true; this.busyLabel = label; this.message = ''; this.isError = false;
    try { await action(); }
    catch (error) { this.message = errorMessage(error as Error); this.isError = true; }
    finally { this.busy = false; this.busyLabel = ''; }
  }

  private apply(reply: NativeReply): void { if (reply.state) { this.data = reply.state; } }
  private editable(): boolean { return this.ready && !this.busy && !this.data.runtime.active; }
  private enabledCount(): number { return this.data.applications.filter((app: ProxyApplication) => app.enabled).length; }

  private selectedName(): string {
    const node = this.data.nodes.find((item: ProxyNode) => item.id === this.data.selected_node_id);
    return node ? node.name : '尚未选择节点';
  }

  private statusText(): string {
    if (!this.ready) { return '尚未就绪'; }
    if (this.data.runtime.phase === 'running' && this.data.runtime.active) { return '代理运行中'; }
    if (this.data.runtime.active) { return '后台运行待确认'; }
    return '未连接';
  }

  private visibleNodes(): ProxyNode[] {
    const search = this.nodeSearch.trim().toLowerCase();
    return this.data.nodes.filter((node: ProxyNode) =>
      `${node.name} ${node.type} ${node.server}`.toLowerCase().includes(search)).slice(0, 100);
  }

  private toggleVPN(): void {
    this.perform(this.data.runtime.active ? '正在停止代理' : '正在连接', async () => {
      const controller = this.controller;
      if (!controller || !this.ready) { throw new Error('应用尚未就绪，请重试'); }
      this.data = this.data.runtime.active ? await controller.stop() : await controller.start();
    });
  }

  private importNodes(): void {
    this.perform('导入节点', async () => {
      const client = this.requireClient();
      const url = this.importRemote ? this.sourceURL.trim() : '';
      const content = this.importRemote ? await downloadSubscription(url) : this.sourceContent.trim();
      const reply = await client.importContent(this.sourceName.trim(), content, url);
      this.apply(reply); this.message = `已导入 ${reply.imported || 0} 个节点`;
      if (reply.warnings && reply.warnings.length > 0) { this.message += `；${reply.warnings[0]}`; }
      this.sourceContent = ''; this.sourceURL = ''; this.sourceName = ''; this.importOpen = false;
    });
  }

  private refreshSource(source: Subscription): void {
    this.perform('更新订阅', async () => {
      const client = this.requireClient();
      const url = await client.sourceURL(source.id);
      if (!url) { throw new Error('此订阅来自粘贴内容，请重新导入'); }
      const reply = await client.importContent(source.name, await downloadSubscription(url), url, source.id);
      this.apply(reply); this.message = `已更新 ${reply.imported || 0} 个节点`;
      if (reply.warnings && reply.warnings.length > 0) { this.message += `；${reply.warnings[0]}`; }
    });
  }

  private removeSource(source: Subscription): void {
    this.getUIContext().showAlertDialog({
      title: '删除订阅', message: `删除“${source.name}”及其全部节点？`,
      primaryButton: { value: '取消', action: () => {} },
      secondaryButton: { value: '删除', action: () => {
        this.perform('删除订阅', async () => { this.apply(await this.requireClient().removeSource(source.id)); });
      } }
    });
  }

  private selectNode(node: ProxyNode): void {
    this.perform('选择节点', async () => { this.apply(await this.requireClient().selectNode(node.id)); });
  }

  private addApplication(): void {
    this.perform('添加应用', async () => {
      this.apply(await this.requireClient().saveApplication({
        bundle_name: this.appBundle.trim(), name: this.appName.trim(), enabled: true
      }));
      this.appName = ''; this.appBundle = ''; this.appFormOpen = false;
    });
  }

  private toggleApplication(app: ProxyApplication, enabled: boolean): void {
    if (!this.editable() || app.enabled === enabled) { return; }
    this.perform('保存应用范围', async () => {
      this.apply(await this.requireClient().saveApplication({ bundle_name: app.bundle_name, name: app.name, enabled: enabled }));
    });
  }

  private removeApplication(app: ProxyApplication): void {
    this.getUIContext().showAlertDialog({
      title: '移除应用', message: `将“${app.name}”从代理范围中移除？`,
      primaryButton: { value: '取消', action: () => {} },
      secondaryButton: { value: '移除', action: () => {
        this.perform('移除应用', async () => { this.apply(await this.requireClient().removeApplication(app)); });
      } }
    });
  }

  private saveDNS(): void {
    this.perform('保存 DNS', async () => {
      const settings: DNSSettings = { bootstrap_dns: this.bootstrapDNS.trim(), remote_dns: this.remoteDNS.trim() };
      this.apply(await this.requireClient().saveSettings(settings)); this.message = 'DNS 设置已保存'; this.dnsOpen = false;
    });
  }

  @Builder
  private sectionTitle(title: string, detail: string) {
    Column({ space: 5 }) {
      Text(title).fontSize(24).fontWeight(FontWeight.Bold).fontColor(INK)
      Text(detail).fontSize(13).fontColor(MUTED).lineHeight(20)
    }.width('100%').alignItems(HorizontalAlign.Start).margin({ bottom: 20 })
  }

  @Builder
  private tabLabel(label: string, index: number) {
    Column({ space: 6 }) {
      Text(label).fontSize(14).fontWeight(this.tab === index ? FontWeight.Bold : FontWeight.Normal)
        .fontColor(this.tab === index ? BLUE : MUTED)
      Rect().width(18).height(3).radius(2).fill(this.tab === index ? BLUE : Color.Transparent)
    }.height(58).justifyContent(FlexAlign.Center)
  }

  @Builder
  private emptyNotice(text: string) {
    Text(text).fontSize(14).fontColor(MUTED).lineHeight(23).width('100%').padding({ top: 32, bottom: 32 })
  }

  @Builder
  private trafficValue(title: string, rate: number, total: number) {
    Column({ space: 7 }) {
      Text(title).fontSize(12).fontColor(MUTED)
      Text(formatBytes(rate) + '/s').fontSize(21).fontWeight(FontWeight.Medium).fontColor(INK)
      Text('累计 ' + formatBytes(total)).fontSize(12).fontColor(MUTED)
    }.alignItems(HorizontalAlign.Start).layoutWeight(1)
  }

  @Builder
  private overview() {
    Scroll() {
      Column({ space: 0 }) {
        Row({ space: 8 }) {
          Circle().width(8).height(8).fill(this.data.runtime.active ? BLUE : '#AEB7C6')
          Text(this.statusText()).fontSize(14).fontColor(MUTED)
        }.width('100%').margin({ top: 16, bottom: 22 })
        Text(this.data.runtime.active ? this.statusText() : '按应用连接')
          .fontSize(34).fontWeight(FontWeight.Bold).fontColor(INK).width('100%')
        Text(this.data.runtime.active && this.data.runtime.phase === 'running' ?
          `${this.enabledCount()} 个应用使用 ${this.selectedName()}` :
          (this.data.runtime.active ? '后台状态暂未确认，可停止代理后重试。' : '选择节点和应用，开启你的代理连接。'))
          .fontSize(15).fontColor(MUTED).lineHeight(24).width('100%').margin({ top: 10, bottom: 26 })
        Button(this.busy ? this.busyLabel : (this.data.runtime.active ? '停止代理' : '开启代理'))
          .width('100%').height(54).borderRadius(14).backgroundColor(BLUE).fontColor(Color.White).fontSize(17)
          .enabled(this.ready && !this.busy).onClick(() => { this.toggleVPN(); })
        if (this.data.runtime.error) {
          Text(this.data.runtime.error).fontSize(13).fontColor('#B04439').lineHeight(20).width('100%').margin({ top: 16 })
        }
        Row() {
          Column({ space: 7 }) {
            Text('当前节点').fontSize(12).fontColor(MUTED)
            Text(this.selectedName()).fontSize(16).fontWeight(FontWeight.Medium).fontColor(INK).maxLines(2)
          }.alignItems(HorizontalAlign.Start).layoutWeight(1)
          Text('更换 ›').fontSize(14).fontColor(BLUE)
        }.width('100%').padding({ top: 26, bottom: 22 }).onClick(() => { this.tab = 1; })
        Divider().color(LINE)
        Row() {
          Text('代理应用').fontSize(15).fontColor(INK).layoutWeight(1)
          Text(`${this.enabledCount()} 个已启用 ›`).fontSize(14).fontColor(BLUE)
        }.width('100%').padding({ top: 22, bottom: 22 }).onClick(() => { this.tab = 2; })
        Divider().color(LINE)
        Text('最近一次连接').fontSize(13).fontColor(MUTED).width('100%').margin({ top: 26, bottom: 16 })
        Row({ space: 16 }) {
          this.trafficValue('↓ 下载', this.data.runtime.download_rate, this.data.runtime.download)
          this.trafficValue('↑ 上传', this.data.runtime.upload_rate, this.data.runtime.upload)
        }.width('100%').margin({ bottom: 28 })
        Divider().color(LINE)
        Text(this.dnsOpen ? '收起 DNS 设置 −' : 'DNS 设置 +').fontSize(14).fontColor(BLUE)
          .width('100%').padding({ top: 22, bottom: 16 }).onClick(() => { this.dnsOpen = !this.dnsOpen; })
        if (this.dnsOpen) {
          Column({ space: 12 }) {
            Text('节点域名解析（直连）').fontSize(12).fontColor(MUTED).width('100%')
            TextInput({ text: this.bootstrapDNS, placeholder: '223.5.5.5' }).height(46).enabled(this.editable())
              .onChange((value: string) => { this.bootstrapDNS = value; })
            Text('应用 DNS（通过节点使用 HTTPS）').fontSize(12).fontColor(MUTED).width('100%')
            TextInput({ text: this.remoteDNS, placeholder: '1.1.1.1' }).height(46).enabled(this.editable())
              .onChange((value: string) => { this.remoteDNS = value; })
            Button('保存设置').height(44).backgroundColor(BLUE).enabled(this.editable()).onClick(() => { this.saveDNS(); })
          }.width('100%').alignItems(HorizontalAlign.Start)
        }
      }.width('100%').alignItems(HorizontalAlign.Start).padding({ left: 24, right: 24, bottom: 32 })
    }.scrollBar(BarState.Off).width('100%').height('100%')
  }

  @Builder
  private nodesPage() {
    Scroll() {
      Column({ space: 0 }) {
        this.sectionTitle('节点', `${this.data.nodes.length} 个节点 · 所选应用共用当前节点`)
        Button(this.importOpen ? '收起导入' : '添加订阅或节点').width('100%').height(46).backgroundColor(BLUE)
          .enabled(this.editable()).onClick(() => { this.importOpen = !this.importOpen; })
        if (this.importOpen) {
          Column({ space: 12 }) {
            Row({ space: 12 }) {
              Button('订阅地址').backgroundColor(this.importRemote ? BLUE : '#EDF1F9').fontColor(this.importRemote ? Color.White : INK)
                .onClick(() => { this.importRemote = true; })
              Button('粘贴内容').backgroundColor(this.importRemote ? '#EDF1F9' : BLUE).fontColor(this.importRemote ? INK : Color.White)
                .onClick(() => { this.importRemote = false; })
            }
            TextInput({ text: this.sourceName, placeholder: '名称（可选）' }).maxLength(100).onChange((value: string) => { this.sourceName = value; })
            if (this.importRemote) {
              TextInput({ text: this.sourceURL, placeholder: 'https:// 订阅地址' }).maxLength(8192).onChange((value: string) => { this.sourceURL = value; })
            } else {
              TextArea({ text: this.sourceContent, placeholder: '节点链接、Base64、Clash 或 sing-box 配置' }).height(160)
                .maxLength(8 * 1024 * 1024).onChange((value: string) => { this.sourceContent = value; })
            }
            Button(this.busy ? this.busyLabel : '导入').width('100%').height(44).backgroundColor(BLUE)
              .enabled(this.editable()).onClick(() => { this.importNodes(); })
          }.width('100%').padding({ top: 16, bottom: 20 })
        }
        ForEach(this.data.sources, (source: Subscription) => {
          Row({ space: 10 }) {
            Column({ space: 4 }) {
              Text(source.name).fontSize(15).fontColor(INK).maxLines(1)
              Text(source.url ? '在线订阅' : '本地导入').fontSize(12).fontColor(MUTED)
            }.alignItems(HorizontalAlign.Start).layoutWeight(1)
            if (source.url) {
              Button('更新').fontSize(12).backgroundColor('#EDF1F9').fontColor(BLUE).enabled(this.editable())
                .onClick(() => { this.refreshSource(source); })
            }
            Button('删除').fontSize(12).backgroundColor('#F3F4F7').fontColor(MUTED).enabled(this.editable()).onClick(() => { this.removeSource(source); })
          }.width('100%').padding({ top: 16, bottom: 16 })
          Divider().color(LINE)
        }, (source: Subscription) => source.id)
        if (this.data.nodes.length === 0) {
          this.emptyNotice('添加订阅地址，或粘贴已有的节点配置。')
        } else {
          TextInput({ text: this.nodeSearch, placeholder: '搜索节点名称、协议或地址' }).margin({ top: 20, bottom: 8 })
            .onChange((value: string) => { this.nodeSearch = value; })
          ForEach(this.visibleNodes(), (node: ProxyNode) => {
            Row({ space: 12 }) {
              Column({ space: 5 }) {
                Text(node.name).fontSize(16).fontWeight(FontWeight.Medium).fontColor(INK).maxLines(2)
                Text(`${node.type.toUpperCase()} · ${node.server}:${node.port}`).fontSize(12).fontColor(MUTED).maxLines(1)
              }.alignItems(HorizontalAlign.Start).layoutWeight(1)
              Text(node.id === this.data.selected_node_id ? '已选' : '选择').fontSize(13).fontColor(node.id === this.data.selected_node_id ? BLUE : MUTED)
            }.width('100%').padding({ top: 17, bottom: 17 }).enabled(this.editable()).onClick(() => { this.selectNode(node); })
            Divider().color(LINE)
          }, (node: ProxyNode) => node.id)
          if (this.visibleNodes().length === 0) { this.emptyNotice('没有匹配的节点。') }
          if (this.data.nodes.length > 100) {
            Text('每次显示前 100 个匹配结果，可搜索其他节点。').fontSize(12).fontColor(MUTED).margin({ top: 18 })
          }
        }
      }.width('100%').alignItems(HorizontalAlign.Start).padding(24)
    }.scrollBar(BarState.Off).width('100%').height('100%')
  }

  @Builder
  private applicationsPage() {
    Scroll() {
      Column({ space: 0 }) {
        this.sectionTitle('应用范围', '只代理已启用的应用，局域网连接直接访问。')
        Button(this.appFormOpen ? '收起添加' : '添加应用').width('100%').height(46).backgroundColor(BLUE)
          .enabled(this.editable()).onClick(() => { this.appFormOpen = !this.appFormOpen; })
        if (this.appFormOpen) {
          Column({ space: 12 }) {
            TextInput({ text: this.appName, placeholder: '应用名称（可选）' }).maxLength(100).onChange((value: string) => { this.appName = value; })
            TextInput({ text: this.appBundle, placeholder: '应用包名，如 com.example.app' }).maxLength(128).onChange((value: string) => { this.appBundle = value; })
            Text('请输入手机上已安装应用的准确包名，可向应用开发方查询。').fontSize(12).fontColor(MUTED).lineHeight(19)
            Button('加入代理范围').width('100%').height(44).backgroundColor(BLUE).enabled(this.editable()).onClick(() => { this.addApplication(); })
          }.width('100%').padding({ top: 16, bottom: 20 })
        }
        if (this.data.applications.length === 0) { this.emptyNotice('先添加至少一个应用，再开启代理。') }
        ForEach(this.data.applications, (app: ProxyApplication) => {
          Column({ space: 8 }) {
            Row({ space: 12 }) {
              Column({ space: 5 }) {
                Text(app.name).fontSize(16).fontWeight(FontWeight.Medium).fontColor(INK)
                Text(app.bundle_name).fontSize(12).fontColor(MUTED).maxLines(2)
              }.alignItems(HorizontalAlign.Start).layoutWeight(1)
              Toggle({ type: ToggleType.Switch, isOn: app.enabled }).selectedColor(BLUE).enabled(this.editable())
                .onChange((enabled: boolean) => { this.toggleApplication(app, enabled); })
            }.width('100%')
            Button('移除').fontSize(12).backgroundColor('#F3F4F7').fontColor(MUTED).enabled(this.editable()).onClick(() => { this.removeApplication(app); })
          }.width('100%').alignItems(HorizontalAlign.Start).padding({ top: 20, bottom: 18 })
          Divider().color(LINE)
        }, (app: ProxyApplication) => app.bundle_name)
      }.width('100%').alignItems(HorizontalAlign.Start).padding(24)
    }.scrollBar(BarState.Off).width('100%').height('100%')
  }

  @Builder
  private activityPage() {
    Scroll() {
      Column({ space: 0 }) {
        this.sectionTitle('活动', '查看当前连接与最近运行日志')
        Row({ space: 12 }) {
          Button(`连接 ${this.data.runtime.connections.length}`).backgroundColor(this.activityLogs ? '#EDF1F9' : BLUE)
            .fontColor(this.activityLogs ? INK : Color.White).onClick(() => { this.activityLogs = false; })
          Button('日志').backgroundColor(this.activityLogs ? BLUE : '#EDF1F9').fontColor(this.activityLogs ? Color.White : INK)
            .onClick(() => { this.activityLogs = true; })
        }.width('100%').margin({ bottom: 16 })
        if (this.activityLogs) {
          if (this.data.runtime.logs.length === 0) { this.emptyNotice('暂无运行日志。') }
          ForEach(this.data.runtime.logs.slice(-80), (entry: LogEntry) => {
            Column({ space: 6 }) {
              Text(`${entry.time.slice(11, 19)} · ${entry.level}`).fontSize(11).fontColor(MUTED)
              Text(entry.message).fontSize(13).fontColor(INK).lineHeight(21).wordBreak(WordBreak.BREAK_ALL)
            }.width('100%').alignItems(HorizontalAlign.Start).padding({ top: 13, bottom: 13 })
            Divider().color(LINE)
          }, (entry: LogEntry, index: number) => `${entry.time}-${index}`)
        } else {
          if (this.data.runtime.connections.length === 0) { this.emptyNotice('暂无活动连接。开启代理并使用所选应用后，连接会显示在这里。') }
          ForEach(this.data.runtime.connections, (connection: LiveConnection) => {
            Column({ space: 6 }) {
              Text(connection.destination).fontSize(14).fontColor(INK).maxLines(2)
              Text(`${connection.network.toUpperCase()} · ↓ ${formatBytes(connection.download)} · ↑ ${formatBytes(connection.upload)}`)
                .fontSize(12).fontColor(MUTED)
            }.width('100%').alignItems(HorizontalAlign.Start).padding({ top: 15, bottom: 15 })
            Divider().color(LINE)
          }, (connection: LiveConnection) => connection.id)
        }
      }.width('100%').alignItems(HorizontalAlign.Start).padding(24)
    }.scrollBar(BarState.Off).width('100%').height('100%')
  }

  build() {
    Column({ space: 0 }) {
      Row({ space: 10 }) {
        Image($r('app.media.app_icon')).width(28).height(28)
        Text('AppForce').fontSize(22).fontWeight(FontWeight.Bold).fontColor(INK).layoutWeight(1)
        Text('鸿蒙版').fontSize(12).fontColor(MUTED)
      }.width('100%').height(66).padding({ left: 24, right: 24 })
      if (this.message) {
        Row({ space: 10 }) {
          Text(this.message).fontSize(13).fontColor(this.isError ? '#B04439' : INK).lineHeight(20).layoutWeight(1)
          if (!this.ready) {
            Button('重试').fontSize(12).backgroundColor(BLUE).enabled(!this.busy).onClick(() => { this.initialize(); })
          } else {
            Text('关闭').fontSize(12).fontColor(MUTED).padding(8).onClick(() => { this.message = ''; })
          }
        }.width('100%').padding({ left: 24, right: 16, top: 10, bottom: 10 }).backgroundColor('#F6F8FC')
      }
      if (this.data.runtime.active && this.tab !== 0) {
        Text('代理运行中，停止后可修改节点和应用范围。').fontSize(12).fontColor(MUTED)
          .width('100%').padding({ left: 24, right: 24, top: 8, bottom: 8 })
      }
      Tabs({ barPosition: BarPosition.End, index: this.tab }) {
        TabContent() { this.overview() }.tabBar(this.tabLabel('连接', 0))
        TabContent() { this.nodesPage() }.tabBar(this.tabLabel('节点', 1))
        TabContent() { this.applicationsPage() }.tabBar(this.tabLabel('应用', 2))
        TabContent() { this.activityPage() }.tabBar(this.tabLabel('活动', 3))
      }.barMode(BarMode.Fixed).barHeight(62).divider({ strokeWidth: 1, color: LINE })
        .layoutWeight(1).animationDuration(180).onChange((index: number) => { this.tab = index; })
    }.width('100%').height('100%').backgroundColor(Color.White)
  }
}
