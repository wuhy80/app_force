export interface Node { id: string; source_id: string; name: string; type: string; server: string; port: number }
export interface Rule { id: string; name: string; path: string; enabled: boolean }
export interface Source { id: string; name: string; url?: string; updated_at: string; warnings: string[] }
export interface Device { id: string; name: string; url: string; token?: string; certificate_sha256?: string }
export interface Settings { mode: 'tun' | 'mixed'; mixed_port: number; dns_mode: 'direct' | 'proxy'; dns_address: string; strict_route: boolean; auto_redirect: boolean }
export interface Connection { id: string; network: string; destination: string; process_path: string; pid: number; proxied: boolean; upload: number; download: number }
export interface State {
  version: string; hostname: string; os: string; nodes: Node[]; sources: Source[]; rules: Rule[]; devices: Device[];
  selected_node_id: string; settings: Settings;
  engine: { running: boolean; mode: string; started_at: string | null; error: string; upload: number; download: number; connections: Connection[] }
}
export interface Process { pid: number; parent_pid: number; name: string; path: string; selectable: boolean }
export interface LogEntry { time: string; level: string; message: string }

declare global {
  interface Window { go?: { main?: { Desktop?: {
    Request(method: string, path: string, body: string): Promise<{ status: number; body: string }>;
    ChooseProgram(): Promise<string>;
  } } } }
}

export const desktop = () => window.go?.main?.Desktop
const fragment = new URLSearchParams(location.hash.slice(1))
let token = fragment.get('token') || sessionStorage.getItem('appforce-token') || ''
if (fragment.has('token')) { sessionStorage.setItem('appforce-token', token); history.replaceState(null, '', location.pathname + location.search) }
export function setToken(value: string) { token = value.trim(); sessionStorage.setItem('appforce-token', token) }
export class APIError extends Error {
  constructor(message: string, public status: number, public warnings: string[] = []) { super(message) }
}
export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  let status: number, data: { error?: string; warnings?: string[] } & T
  const bridge = desktop()
  if (bridge) {
    const reply = await bridge.Request(method, path, body === undefined ? '' : JSON.stringify(body))
    status = reply.status
    data = JSON.parse(reply.body)
  } else {
    const response = await fetch(path, {
      method, headers: { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(65000),
    })
    status = response.status
    data = await response.json()
  }
  if (status >= 400) throw new APIError(data.error || `请求失败 (${status})`, status, data.warnings || [])
  return data
}
