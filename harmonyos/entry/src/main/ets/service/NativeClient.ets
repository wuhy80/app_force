import native from 'libappforce.so';
import { DNSSettings, MobileState, NativeReply, NativeRequest, ProxyApplication } from '../model/Types';

export class NativeClient {
  readonly directory: string;

  constructor(filesDir: string) {
    this.directory = `${filesDir}/appforce`;
  }

  static parse(raw: string): NativeReply {
    const reply = JSON.parse(raw) as NativeReply;
    if (!reply.ok) { throw new Error(reply.error || '代理内核操作失败'); }
    return reply;
  }

  async request(request: NativeRequest): Promise<NativeReply> {
    return NativeClient.parse(await native.call(JSON.stringify(request)));
  }

  async initialize(bundleName: string): Promise<MobileState> {
    const reply = await this.request({ method: 'initialize', directory: this.directory, self_bundle: bundleName });
    if (!reply.state) { throw new Error('无法读取应用配置'); }
    return reply.state;
  }

  async state(): Promise<MobileState> {
    const reply = await this.request({ method: 'state', directory: this.directory });
    if (!reply.state) { throw new Error('无法读取代理状态'); }
    return reply.state;
  }

  async importContent(name: string, content: string, url: string = '', sourceId: string = ''): Promise<NativeReply> {
    return this.request({ method: 'import', directory: this.directory, name: name, content: content, url: url, source_id: sourceId });
  }

  async selectNode(id: string): Promise<NativeReply> {
    return this.request({ method: 'selectNode', directory: this.directory, node_id: id });
  }

  async saveApplication(app: ProxyApplication): Promise<NativeReply> {
    return this.request({ method: 'saveApplication', directory: this.directory, application: app });
  }

  async removeApplication(app: ProxyApplication): Promise<NativeReply> {
    return this.request({ method: 'removeApplication', directory: this.directory, application: app });
  }

  async saveSettings(settings: DNSSettings): Promise<NativeReply> {
    return this.request({ method: 'saveSettings', directory: this.directory, settings: settings });
  }

  async removeSource(sourceId: string): Promise<NativeReply> {
    return this.request({ method: 'removeSource', directory: this.directory, source_id: sourceId });
  }

  async sourceURL(sourceId: string): Promise<string> {
    const reply = await this.request({ method: 'sourceURL', directory: this.directory, source_id: sourceId });
    return reply.source_url || '';
  }

  async prepareVPN(): Promise<NativeReply> {
    return this.request({ method: 'prepareVpn', directory: this.directory });
  }

  async requestStop(): Promise<NativeReply> {
    return this.request({ method: 'requestStop', directory: this.directory });
  }

  async recordFailure(message: string): Promise<NativeReply> {
    return this.request({ method: 'recordFailure', directory: this.directory, error: message });
  }
}
