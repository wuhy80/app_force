package wfp

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	fwpuclnt   = windows.NewLazySystemDLL("fwpuclnt.dll")
	getAppID   = fwpuclnt.NewProc("FwpmGetAppIdFromFileName0")
	freeMemory = fwpuclnt.NewProc("FwpmFreeMemory0")
)

// mstcpip.h uses _WSAIOW(IOC_VENDOR, 220/221/222) for all three IOCTLs,
// including the queries (the names do not imply IOC_OUT).
const (
	queryRecords = 0x980000DC
	queryContext = 0x980000DD
	setRecords   = 0x980000DE
	setPolicy    = 0x0012A004 // FILE_DEVICE_NETWORK, function 0x801, METHOD_BUFFERED, FILE_WRITE_ACCESS
)

func applicationID(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var blob *struct {
		Size uint32
		Data *uint16
	}
	code, _, _ := getAppID.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&blob)))
	if code != 0 {
		return "", windows.Errno(code)
	}
	defer freeMemory.Call(uintptr(unsafe.Pointer(&blob)))
	if blob == nil || blob.Size == 0 || blob.Size%2 != 0 || blob.Size > (MaxPath+1)*2 {
		return "", fmt.Errorf("WFP 程序标识长度无效")
	}
	chars := unsafe.Slice(blob.Data, blob.Size/2)
	return windows.UTF16ToString(chars), nil
}

type device struct{ handle windows.Handle }

func openDriver() (driver, error) {
	p, _ := windows.UTF16PtrFromString(`\\.\AppForceWfp`)
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("无法打开 AppForceWfp 驱动；请先安装并启动已签名的驱动，以管理员身份运行 AppForce（不会回退 TUN）: %w", err)
	}
	return &device{h}, nil
}

func (d *device) Close() error { return windows.CloseHandle(d.handle) }
func (d *device) Activate(b []byte) error {
	var returned uint32
	if err := windows.DeviceIoControl(d.handle, setPolicy, &b[0], uint32(len(b)), nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("启用 WFP 规则失败，检查驱动版本和 BFE 服务: %w", err)
	}
	return nil
}

func ioctl(raw syscall.RawConn, code uint32, input, output []byte) (uint32, error) {
	var returned uint32
	var callErr error
	var in, out *byte
	if len(input) != 0 {
		in = &input[0]
	}
	if len(output) != 0 {
		out = &output[0]
	}
	err := raw.Control(func(fd uintptr) {
		callErr = windows.WSAIoctl(windows.Handle(fd), code, in, uint32(len(input)), out, uint32(len(output)), &returned, nil, 0)
	})
	if err != nil {
		return 0, err
	}
	return returned, callErr
}

func queryRedirect(c net.Conn) ([]byte, []byte, error) {
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	ctx := make([]byte, ContextSize)
	n, err := ioctl(raw, queryContext, nil, ctx)
	if err != nil || n != ContextSize {
		return nil, nil, fmt.Errorf("拒绝没有有效 WFP 上下文的连接: %v", err)
	}
	// Opaque records can grow with other callouts. Retry only a bounded required
	// size returned by Winsock; never accept missing or truncated records.
	records := make([]byte, 4096)
	n, err = ioctl(raw, queryRecords, nil, records)
	if err != nil && n > uint32(len(records)) && n <= 1<<20 {
		records = make([]byte, n)
		n, err = ioctl(raw, queryRecords, nil, records)
	}
	if err != nil || n == 0 || n > uint32(len(records)) {
		return nil, nil, fmt.Errorf("读取 WFP 重定向记录失败: %v", err)
	}
	return ctx, records[:n], nil
}

func socketControl(ctx context.Context) func(context.Context, string, string, syscall.RawConn) error {
	return func(_ context.Context, _, _ string, raw syscall.RawConn) error {
		records := Records(ctx)
		if len(records) == 0 {
			return nil
		} // explicit local proxy/account requests
		_, err := ioctl(raw, setRecords, records, nil)
		if err != nil {
			return fmt.Errorf("设置 WFP 上游重定向记录失败: %w", err)
		}
		return nil
	}
}
