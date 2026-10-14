export interface ProxyNode {
  id: string;
  source_id: string;
  name: string;
  type: string;
  server: string;
  port: number;
}

export interface Subscription {
  id: string;
  name: string;
  url?: string;
  updated_at: string;
  warnings: string[];
}

export interface ProxyApplication {
  bundle_name: string;
  name: string;
  enabled: boolean;
}

export interface DNSSettings {
  bootstrap_dns: string;
  remote_dns: string;
}

export interface LiveConnection {
  id: string;
  network: string;
  destination: string;
  upload: number;
  download: number;
}

export interface LogEntry {
  time: string;
  level: string;
  message: string;
}

export interface RuntimeState {
  phase: string;
  active: boolean;
  session_id: string;
  revision: number;
  updated_at: number;
  started_at: number;
  error: string;
  upload: number;
  download: number;
  upload_rate: number;
  download_rate: number;
  connections: LiveConnection[];
  logs: LogEntry[];
}

export interface MobileState {
  version: string;
  revision: number;
  self_bundle: string;
  sources: Subscription[];
  nodes: ProxyNode[];
  selected_node_id: string;
  applications: ProxyApplication[];
  settings: DNSSettings;
  runtime: RuntimeState;
}

export interface VPNPlan {
  applications: string[];
  ipv4: string;
  ipv6: string;
  dns: string;
  mtu: number;
}

export interface NativeReply {
  ok: boolean;
  error?: string;
  state?: MobileState;
  vpn?: VPNPlan;
  imported?: number;
  warnings?: string[];
  source_url?: string;
}

export interface NativeRequest {
  method: string;
  directory: string;
  name?: string;
  content?: string;
  source_id?: string;
  url?: string;
  node_id?: string;
  self_bundle?: string;
  application?: ProxyApplication;
  settings?: DNSSettings;
  error?: string;
}

export function emptyState(): MobileState {
  return {
    version: '0.1.0', revision: 0, self_bundle: 'com.appforce.mobile',
    sources: [], nodes: [], selected_node_id: '', applications: [],
    settings: { bootstrap_dns: '223.5.5.5', remote_dns: '1.1.1.1' },
    runtime: {
      phase: 'stopped', active: false, session_id: '', revision: 0,
      updated_at: 0, started_at: 0, error: '', upload: 0, download: 0,
      upload_rate: 0, download_rate: 0, connections: [], logs: []
    }
  };
}

export function errorMessage(error: Error): string {
  return error.message || '操作失败，请重试';
}

export function formatBytes(value: number): string {
  if (value < 1024) { return `${value} B`; }
  if (value < 1024 * 1024) { return `${(value / 1024).toFixed(1)} KB`; }
  if (value < 1024 * 1024 * 1024) { return `${(value / 1024 / 1024).toFixed(1)} MB`; }
  return `${(value / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
