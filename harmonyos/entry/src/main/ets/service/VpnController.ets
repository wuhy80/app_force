import { vpnExtension } from '@kit.NetworkKit';
import { Want } from '@kit.AbilityKit';
import { MobileState } from '../model/Types';
import { NativeClient } from './NativeClient';

function pause(milliseconds: number): Promise<void> {
  return new Promise<void>((resolve) => { setTimeout(resolve, milliseconds); });
}

export class VpnController {
  private readonly client: NativeClient;
  private readonly want: Want;

  constructor(client: NativeClient, bundleName: string) {
    this.client = client;
    this.want = { bundleName: bundleName, abilityName: 'AppForceVpnAbility' };
  }

  async start(): Promise<MobileState> {
    await this.client.prepareVPN();
    const started = Date.now();
    await vpnExtension.startVpnExtensionAbility(this.want);
    try {
      for (let attempt = 0; attempt < 40; attempt++) {
        const state = await this.client.state();
        if (state.runtime.active && state.runtime.phase === 'running') { return state; }
        if (state.runtime.phase === 'error' && state.runtime.updated_at >= started) {
          throw new Error(state.runtime.error || '代理启动失败');
        }
        await pause(500);
      }
      throw new Error('VPN 未启动，请检查系统授权后重试');
    } catch (error) {
      // A denied authorization or failed core start must not leave an empty VPN.
      try { await this.stop(); }
      catch (_) { throw new Error(`${(error as Error).message || '代理启动失败'}；请检查系统 VPN 设置并关闭残留连接`); }
      throw error;
    }
  }

  async stop(): Promise<MobileState> {
    let state = await this.client.state();
    if (state.runtime.active) {
      await this.client.requestStop();
      for (let attempt = 0; attempt < 12; attempt++) {
        await pause(500);
        state = await this.client.state();
        if (!state.runtime.active) { break; }
      }
    }
    // Also tears down the OS interface if the core has already failed or exited.
    try { await vpnExtension.stopVpnExtensionAbility(this.want); }
    catch (error) {
      state = await this.client.state();
      if (state.runtime.active) { throw error; }
      throw new Error('内核已停止，但系统 VPN 未确认退出，请在系统设置中关闭 VPN 后重试');
    }
    state = await this.client.state();
    if (state.runtime.active) { throw new Error('后台尚未停止，请稍后重试'); }
    return state;
  }
}
