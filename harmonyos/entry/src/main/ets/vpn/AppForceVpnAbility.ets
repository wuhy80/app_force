import { VpnExtensionAbility, vpnExtension } from '@kit.NetworkKit';
import { fileIo } from '@kit.CoreFileKit';
import native from 'libappforce.so';
import { NativeClient } from '../service/NativeClient';
import { VPNPlan } from '../model/Types';

function vpnConfiguration(plan: VPNPlan): vpnExtension.VpnConfig {
  return {
    addresses: [
      { address: { address: plan.ipv4, family: 1 }, prefixLength: 30 },
      { address: { address: plan.ipv6, family: 2 }, prefixLength: 126 }
    ],
    routes: [
      { interface: '', destination: { address: { address: '0.0.0.0', family: 1 }, prefixLength: 0 },
        gateway: { address: '0.0.0.0', family: 1 }, hasGateway: false, isDefaultRoute: true },
      { interface: '', destination: { address: { address: '::', family: 2 }, prefixLength: 0 },
        gateway: { address: '::', family: 2 }, hasGateway: false, isDefaultRoute: true }
    ],
    dnsAddresses: [plan.dns],
    mtu: plan.mtu,
    isIPv4Accepted: true,
    isIPv6Accepted: true,
    trustedApplications: plan.applications
  };
}

export default class AppForceVpnAbility extends VpnExtensionAbility {
  private connection?: vpnExtension.VpnConnection;
  private tunFd: number = -1;
  private destroyed: boolean = false;
  private startTask?: Promise<void>;
  private cleanupTask?: Promise<void>;

  onCreate(): void { this.beginStart(); }

  // Some system versions deliver repeat starts through onRequest.
  onRequest(): void { this.beginStart(); }

  private beginStart(): void {
    if (!this.startTask && !this.destroyed) { this.startTask = this.open(); }
  }

  private async open(): Promise<void> {
    const client = new NativeClient(this.context.filesDir);
    try {
      const reply = await client.prepareVPN();
      if (!reply.vpn || this.destroyed) { return; }
      const connection = vpnExtension.createVpnConnection(this.context);
      this.connection = connection;
      this.tunFd = await connection.create(vpnConfiguration(reply.vpn));
      if (this.destroyed) { await this.cleanup(); return; }
      NativeClient.parse(await native.start(client.directory, this.tunFd,
        (socketFd: number): Promise<void> => connection.protect(socketFd)));
      if (this.destroyed) { await this.cleanup(); }
    } catch (error) {
      await this.cleanup();
      const failure = error as Error;
      try { await client.recordFailure(failure.message || 'VPN 启动失败'); } catch (_) {}
    }
  }

  onDestroy(): void {
    this.destroyed = true;
    // Keep the original descriptor alive until an in-flight native start ends.
    if (this.startTask) { this.startTask.then(() => { this.cleanup(); }); }
    else { this.cleanup(); }
  }

  private cleanup(): Promise<void> {
    if (!this.cleanupTask) { this.cleanupTask = this.close(); }
    return this.cleanupTask;
  }

  private async close(): Promise<void> {
    try { NativeClient.parse(await native.stop()); } catch (_) {}
    if (this.connection) {
      try { await this.connection.destroy(); } catch (_) {}
    }
    if (this.tunFd > 0) {
      try { fileIo.closeSync(this.tunFd); } catch (_) {}
      this.tunFd = -1;
    }
  }
}
