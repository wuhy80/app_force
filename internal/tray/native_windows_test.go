//go:build windows

package tray

import (
	"testing"
	"unsafe"
)

func TestNativeStructLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("Windows releases target amd64 and arm64")
	}
	if size := unsafe.Sizeof(notifyIconData{}); size != 976 {
		t.Fatalf("NOTIFYICONDATAW size = %d, want 976", size)
	}
	if size := unsafe.Sizeof(windowClass{}); size != 80 {
		t.Fatalf("WNDCLASSEXW size = %d, want 80", size)
	}
	if size := unsafe.Sizeof(message{}); size != 48 {
		t.Fatalf("MSG size = %d, want 48", size)
	}
}
