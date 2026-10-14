//go:build cgo && (linux || darwin || openharmony)

package main

/*
#include <stdlib.h>
typedef int (*AppForceProtectFn)(int fd, void *context);
static int appforce_protect(void *callback, int fd, void *context) {
    return ((AppForceProtectFn) callback)(fd, context);
}
*/
import "C"

import (
	"appforce/harmonyos/core/mobile"
	"encoding/json"
	"fmt"
	"unsafe"
)

var runtime mobile.Runtime

func reply(fn func() mobile.Reply) (result *C.char) {
	defer func() {
		if recover() != nil {
			result = C.CString(`{"ok":false,"error":"内核操作异常"}`)
		}
	}()
	data, err := json.Marshal(fn())
	if err != nil {
		return C.CString(`{"ok":false,"error":"状态序列化失败"}`)
	}
	return C.CString(string(data))
}

//export AppForceCall
func AppForceCall(request *C.char) *C.char {
	return reply(func() mobile.Reply {
		if request == nil {
			return mobile.Reply{Error: "请求不能为空"}
		}
		return mobile.Call([]byte(C.GoString(request)))
	})
}

//export AppForceStart
func AppForceStart(directory *C.char, fd C.int, callback unsafe.Pointer, context unsafe.Pointer) *C.char {
	return reply(func() mobile.Reply {
		if directory == nil || fd <= 0 || callback == nil {
			return mobile.Reply{Error: "系统 TUN 或保护回调无效"}
		}
		protect := func(socketFD int) error {
			if C.appforce_protect(callback, C.int(socketFD), context) != 0 {
				return fmt.Errorf("VPN socket 保护失败，连接已终止")
			}
			return nil
		}
		if err := runtime.Start(C.GoString(directory), mobile.DupTUN(int(fd)), protect); err != nil {
			return mobile.Reply{Error: err.Error()}
		}
		result := runtime.Reply()
		if !result.OK {
			// A failed start reply makes the bridge release its callback context.
			// Keep that safe even when the core started but state could not be read.
			_ = runtime.Stop()
		}
		return result
	})
}

//export AppForceStop
func AppForceStop() *C.char {
	return reply(func() mobile.Reply {
		if err := runtime.Stop(); err != nil {
			return mobile.Reply{Error: err.Error()}
		}
		return runtime.Reply()
	})
}

//export AppForceFree
func AppForceFree(pointer unsafe.Pointer) { C.free(pointer) }

func main() {}
