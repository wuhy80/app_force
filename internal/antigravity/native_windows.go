//go:build windows

package antigravity

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	credentialDLL = windows.NewLazySystemDLL("advapi32.dll")
	credRead      = credentialDLL.NewProc("CredReadW")
	credWrite     = credentialDLL.NewProc("CredWriteW")
	credFree      = credentialDLL.NewProc("CredFree")
	credDelete    = credentialDLL.NewProc("CredDeleteW")
)

type windowsCredential struct {
	Flags          uint32
	Type           uint32
	TargetName     *uint16
	Comment        *uint16
	LastWritten    windows.Filetime
	BlobSize       uint32
	Blob           *byte
	Persist        uint32
	AttributeCount uint32
	Attributes     uintptr
	TargetAlias    *uint16
	UserName       *uint16
}

func readSystemCredential(ctx context.Context, target resolvedTarget) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	name, _ := windows.UTF16PtrFromString("gemini:antigravity")
	var pointer *windowsCredential
	ok, _, callErr := credRead.Call(uintptr(unsafe.Pointer(name)), 1, 0, uintptr(unsafe.Pointer(&pointer)))
	if ok == 0 && callErr == windows.ERROR_NOT_FOUND {
		return Credential{}, ErrNoCredential
	}
	if ok == 0 || pointer == nil {
		return Credential{}, fmt.Errorf("Windows 凭据管理器中没有 Antigravity 登录凭据，请先在 Antigravity 2.x 或 agy 登录")
	}
	defer credFree.Call(uintptr(unsafe.Pointer(pointer)))
	if pointer.BlobSize > 65536 {
		return Credential{}, fmt.Errorf("Antigravity 凭据过大")
	}
	data := append([]byte(nil), unsafe.Slice(pointer.Blob, int(pointer.BlobSize))...)
	return parseCredential(string(data))
}

func deleteSystemCredential(ctx context.Context, target resolvedTarget) error {
	name, _ := windows.UTF16PtrFromString("gemini:antigravity")
	ok, _, err := credDelete.Call(uintptr(unsafe.Pointer(name)), 1, 0)
	if ok == 0 && err != windows.ERROR_NOT_FOUND {
		return fmt.Errorf("移除新写入的 Windows 凭据失败")
	}
	return nil
}

func writeSystemCredential(ctx context.Context, target resolvedTarget, c Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data := systemPayload(c)
	if len(data) > 2560 {
		return fmt.Errorf("账号凭据超过 Windows Credential Manager 大小限制")
	}
	name, _ := windows.UTF16PtrFromString("gemini:antigravity")
	username, _ := windows.UTF16PtrFromString("antigravity")
	credential := windowsCredential{Type: 1, TargetName: name, BlobSize: uint32(len(data)), Blob: &data[0], Persist: 2, UserName: username}
	ok, _, _ := credWrite.Call(uintptr(unsafe.Pointer(&credential)), 0)
	if ok == 0 {
		return fmt.Errorf("写入 Windows Antigravity 凭据失败")
	}
	return nil
}
