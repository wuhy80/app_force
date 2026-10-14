//go:build linux || darwin || openharmony

package mobile

import (
	"context"
	"fmt"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/x/list"
	"net"
	"net/netip"
	"os"
	"sync"
)

type TunOpener func(*tun.Options) (tun.Tun, error)
type SocketProtector func(int) error

// The OS selects the physical network after protect(fd). This logical outlet
// keeps sing-box's platform monitor awake without netlink or interface binding.
var protectedOutlet = control.Interface{Index: 0, Name: "platform-protected", MTU: 1500, Flags: net.FlagUp}

type platform struct {
	open    TunOpener
	protect SocketProtector
	monitor *platformMonitor
}

func newPlatform(open TunOpener, protect SocketProtector) *platform {
	return &platform{open: open, protect: protect, monitor: &platformMonitor{}}
}

var _ adapter.PlatformInterface = (*platform)(nil)

func (*platform) Initialize(manager adapter.NetworkManager) error { return manager.UpdateInterfaces() }
func (*platform) UsePlatformAutoDetectInterfaceControl() bool     { return true }
func (p *platform) AutoDetectInterfaceControl(fd int) error {
	if p.protect == nil {
		return fmt.Errorf("VPN socket 保护未接入")
	}
	return p.protect(fd)
}
func (*platform) UsePlatformInterface() bool { return true }
func (p *platform) OpenInterface(options *tun.Options, _ option.TunPlatformOptions) (tun.Tun, error) {
	if p.open == nil {
		return nil, fmt.Errorf("系统 TUN 尚未创建")
	}
	if options.AutoRoute {
		return nil, fmt.Errorf("鸿蒙网卡和路由必须由系统 VPN 管理")
	}
	p.monitor.RegisterMyInterface(options.Name)
	return p.open(options)
}
func (*platform) ProcessPlatformOptions(option.TunPlatformOptions) error { return nil }
func (*platform) UsePlatformDefaultInterfaceMonitor() bool               { return true }
func (p *platform) CreateDefaultInterfaceMonitor(logger.Logger) tun.DefaultInterfaceMonitor {
	return p.monitor
}
func (*platform) UsePlatformNetworkInterfaces() bool { return true }
func (*platform) NetworkInterfaces() ([]adapter.NetworkInterface, error) {
	return []adapter.NetworkInterface{{Interface: protectedOutlet}}, nil
}
func (*platform) UnderNetworkExtension() bool                     { return true }
func (*platform) NetworkExtensionIncludeAllNetworks() bool        { return false }
func (*platform) ClearDNSCache()                                  {}
func (*platform) RequestPermissionForWIFIState() error            { return nil }
func (*platform) ReadWIFIState(context.Context) adapter.WIFIState { return adapter.WIFIState{} }
func (*platform) UsePlatformConnectionOwnerFinder() bool          { return false }
func (*platform) FindConnectionOwner(*adapter.FindConnectionOwnerRequest) (*adapter.ConnectionOwner, error) {
	return nil, os.ErrInvalid
}
func (*platform) UsePlatformWIFIMonitor() bool                 { return true }
func (*platform) UsePlatformNotification() bool                { return false }
func (*platform) SendNotification(*adapter.Notification) error { return os.ErrInvalid }
func (*platform) CancelNotification(string, int32) error       { return os.ErrInvalid }
func (*platform) MyInterfaceAddress() []netip.Addr {
	return []netip.Addr{netip.MustParseAddr(TUNIPv4), netip.MustParseAddr(TUNIPv6)}
}
func (*platform) UsePlatformNeighborResolver() bool                         { return false }
func (*platform) StartNeighborMonitor(adapter.NeighborUpdateListener) error { return os.ErrInvalid }
func (*platform) CloseNeighborMonitor(adapter.NeighborUpdateListener) error { return nil }
func (*platform) UsePlatformShell() bool                                    { return false }
func (*platform) CheckPlatformShell() error                                 { return os.ErrInvalid }
func (*platform) OpenShellSession(*adapter.PlatformUser, string, []string, string, int32, int32) (adapter.ShellSession, error) {
	return nil, os.ErrInvalid
}
func (*platform) LookupUser(string) (*adapter.PlatformUser, error) { return nil, os.ErrInvalid }
func (*platform) LookupSFTPServer() (string, error)                { return "", os.ErrInvalid }
func (*platform) ReadSystemSSHHostKey() ([]byte, error)            { return nil, os.ErrInvalid }
func (*platform) TailscaleHostname() string                        { return "AppForce" }
func (*platform) UsePlatformBridge() bool                          { return false }
func (*platform) CreateBridge(adapter.BridgeOptions) (adapter.BridgeSession, error) {
	return nil, os.ErrInvalid
}

type platformMonitor struct {
	mu    sync.Mutex
	names []string
}

func (*platformMonitor) Start() error                         { return nil }
func (*platformMonitor) Close() error                         { return nil }
func (*platformMonitor) DefaultInterface() *control.Interface { i := protectedOutlet; return &i }
func (*platformMonitor) OverrideAndroidVPN() bool             { return false }
func (*platformMonitor) AndroidVPNEnabled() bool              { return true }
func (*platformMonitor) RegisterCallback(tun.DefaultInterfaceUpdateCallback) *list.Element[tun.DefaultInterfaceUpdateCallback] {
	return nil
}
func (*platformMonitor) UnregisterCallback(*list.Element[tun.DefaultInterfaceUpdateCallback]) {}
func (m *platformMonitor) RegisterMyInterface(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.names = append(m.names, name)
}
func (m *platformMonitor) MyInterfaces() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.names...)
}
