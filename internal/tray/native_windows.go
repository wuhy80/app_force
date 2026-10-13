//go:build windows

package tray

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	registerClass             = user32.NewProc("RegisterClassExW")
	unregisterClass           = user32.NewProc("UnregisterClassW")
	createWindow              = user32.NewProc("CreateWindowExW")
	defWindowProc             = user32.NewProc("DefWindowProcW")
	destroyWindow             = user32.NewProc("DestroyWindow")
	getMessage                = user32.NewProc("GetMessageW")
	translateMessage          = user32.NewProc("TranslateMessage")
	dispatchMessage           = user32.NewProc("DispatchMessageW")
	postMessage               = user32.NewProc("PostMessageW")
	postQuitMessage           = user32.NewProc("PostQuitMessage")
	registerWindowMessage     = user32.NewProc("RegisterWindowMessageW")
	changeWindowMessageFilter = user32.NewProc("ChangeWindowMessageFilterEx")
	loadImage                 = user32.NewProc("LoadImageW")
	createPopupMenu           = user32.NewProc("CreatePopupMenu")
	appendMenu                = user32.NewProc("AppendMenuW")
	destroyMenu               = user32.NewProc("DestroyMenu")
	trackPopupMenu            = user32.NewProc("TrackPopupMenuEx")
	setForegroundWindow       = user32.NewProc("SetForegroundWindow")
	getCursorPos              = user32.NewProc("GetCursorPos")
	shellNotifyIcon           = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
)

const (
	wmClose       = 0x0010
	wmDestroy     = 0x0002
	wmTray        = 0x8001
	wmUpdate      = 0x8002
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4
)

type windowClass struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	SmallIcon                          windows.Handle
}

type point struct{ X, Y int32 }

type message struct {
	Window         windows.Handle
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}

// Timeout and Version are a union in NOTIFYICONDATAW; one uint32 preserves its ABI.
type notifyIconData struct {
	Size                       uint32
	Window                     windows.Handle
	ID, Flags, CallbackMessage uint32
	Icon                       windows.Handle
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	TimeoutOrVersion           uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GUID                       windows.GUID
	BalloonIcon                windows.Handle
}

// Native owns its hidden window and message pump on a dedicated OS thread. All
// Shell and menu operations run there, separately from Wails' main window loop.
type Native struct {
	mu             sync.Mutex
	window         windows.Handle
	view           View
	ready          chan error
	done           chan struct{}
	closeOnce      sync.Once
	onAction       func(Action)
	onExit         func(error)
	icon           notifyIconData
	taskbarCreated uint32
	loopErr        error
}

func StartNative(view View, onAction func(Action), onExit func(error)) (*Native, error) {
	t := &Native{view: view, ready: make(chan error, 1), done: make(chan struct{}), onAction: onAction, onExit: onExit}
	go t.run()
	if err := <-t.ready; err != nil {
		<-t.done
		return nil, err
	}
	return t, nil
}

func (t *Native) Update(view View) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.view == view {
		return
	}
	t.view = view
	if t.window != 0 {
		postMessage.Call(uintptr(t.window), wmUpdate, 0, 0)
	}
}

func (t *Native) currentView() View {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.view
}

func (t *Native) Close() {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.window != 0 {
			postMessage.Call(uintptr(t.window), wmClose, 0, 0)
		}
	})
	<-t.done
}

func winError(operation string, err error) error {
	if err == windows.ERROR_SUCCESS {
		return fmt.Errorf("%s 失败", operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (t *Native) notify(operation uintptr) error {
	result, _, err := shellNotifyIcon.Call(operation, uintptr(unsafe.Pointer(&t.icon)))
	if result == 0 {
		return winError("Shell_NotifyIcon", err)
	}
	return nil
}

func (t *Native) addIcon() error {
	t.updateTip()
	if err := t.notify(nimAdd); err != nil {
		return err
	}
	t.icon.TimeoutOrVersion = 4 // NOTIFYICON_VERSION_4 includes keyboard activation.
	if err := t.notify(nimSetVersion); err != nil {
		_ = t.notify(nimDelete)
		return err
	}
	return nil
}

func (t *Native) updateTip() {
	view := t.currentView()
	text := "AppForce · " + view.StatusText()
	if view.Node != "" {
		text += "\n" + view.Node
	}
	t.icon.Tip = tooltip(text)
}

func (t *Native) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(2, nil, &instance); err != nil {
		t.ready <- err
		return
	}
	className, _ := windows.UTF16PtrFromString("AppForce.Tray")
	class := windowClass{Instance: instance, ClassName: className, WndProc: windows.NewCallback(t.windowProc)}
	class.Size = uint32(unsafe.Sizeof(class))
	if result, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class))); result == 0 {
		t.ready <- winError("注册托盘窗口", err)
		return
	}
	defer unregisterClass.Call(uintptr(unsafe.Pointer(className)), uintptr(instance))
	taskbarMessage, _ := windows.UTF16PtrFromString("TaskbarCreated")
	eventID, _, err := registerWindowMessage.Call(uintptr(unsafe.Pointer(taskbarMessage)))
	if eventID == 0 {
		t.ready <- winError("注册 Explorer 恢复事件", err)
		return
	}
	t.taskbarCreated = uint32(eventID)
	window, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, 0, uintptr(instance), 0)
	if window == 0 {
		t.ready <- winError("创建托盘窗口", err)
		return
	}
	t.mu.Lock()
	t.window = windows.Handle(window)
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		liveWindow := t.window
		t.window = 0
		t.mu.Unlock()
		if liveWindow != 0 {
			_ = t.notify(nimDelete)
			destroyWindow.Call(uintptr(liveWindow))
		}
	}()
	// The executable is elevated for TUN access; Explorer normally is not. UIPI
	// must allow these two Shell messages for clicks and restart recovery.
	for _, msg := range []uint32{wmTray, t.taskbarCreated} {
		if result, _, err := changeWindowMessageFilter.Call(window, uintptr(msg), 1, 0); result == 0 {
			t.ready <- winError("允许托盘 Shell 消息", err)
			return
		}
	}
	// Resource 1 is generated by build/windows; LR_SHARED avoids owning the icon handle.
	icon, _, _ := loadImage.Call(uintptr(instance), 1, 1, 0, 0, 0x8040)
	if icon == 0 {
		icon, _, _ = loadImage.Call(0, 32512, 1, 0, 0, 0x8040)
	}
	if icon == 0 {
		t.ready <- fmt.Errorf("无法加载托盘图标")
		return
	}
	t.icon = notifyIconData{Window: windows.Handle(window), ID: 1, Flags: 0x87, CallbackMessage: wmTray, Icon: windows.Handle(icon)}
	t.icon.Size = uint32(unsafe.Sizeof(t.icon))
	if err := t.addIcon(); err != nil {
		t.ready <- err
		return
	}
	t.ready <- nil
	for {
		var msg message
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			t.loopErr = winError("托盘消息循环", err)
			break
		}
		if result == 0 {
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	if t.onExit != nil {
		t.onExit(t.loopErr)
	}
}

func (t *Native) dispatch(action Action) {
	// UI callbacks can invoke Wails and must never block this thread during shutdown.
	if t.onAction != nil {
		go t.onAction(action)
	}
}

func (t *Native) windowProc(window windows.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmTray:
		switch uint16(lparam) {
		case 0x007b: // WM_CONTEXTMENU, including the keyboard menu key / Shift+F10.
			t.showMenu(window, wparam)
		case 0x0400, 0x0401, 0x0203: // NIN_SELECT, NIN_KEYSELECT, WM_LBUTTONDBLCLK.
			t.dispatch(Show)
		}
		return 0
	case wmUpdate:
		t.updateTip()
		_ = t.notify(nimModify)
		return 0
	case wmClose:
		destroyWindow.Call(uintptr(window))
		return 0
	case 0x0016: // WM_ENDSESSION: honor Windows logoff/shutdown.
		if wparam != 0 {
			t.dispatch(Exit)
		}
		return 0
	case wmDestroy:
		t.mu.Lock()
		t.window = 0
		t.mu.Unlock()
		_ = t.notify(nimDelete)
		postQuitMessage.Call(0)
		return 0
	default:
		if msg == t.taskbarCreated && t.taskbarCreated != 0 {
			if err := t.addIcon(); err != nil {
				t.loopErr = err
				destroyWindow.Call(uintptr(window))
			}
			return 0
		}
	}
	result, _, _ := defWindowProc.Call(uintptr(window), uintptr(msg), wparam, lparam)
	return result
}

func (t *Native) showMenu(window windows.Handle, location uintptr) {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	view := t.currentView()
	add := func(id uintptr, title string, enabled bool) {
		flags := uintptr(0)
		if !enabled {
			flags = 3
		} // MF_GRAYED | MF_DISABLED
		text, _ := windows.UTF16PtrFromString(menuText(title))
		appendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(text)))
	}
	separator := func() { appendMenu.Call(menu, 0x0800, 0, 0) }
	add(0, view.StatusText(), false)
	add(0, view.NodeText(), false)
	add(0, view.ModeText(), false)
	separator()
	add(uintptr(Show)+1, "显示主窗口", true)
	separator()
	add(uintptr(Start)+1, "启动本机代理", view.CanStart())
	add(uintptr(Stop)+1, "停止本机代理", view.CanStop())
	separator()
	add(uintptr(Exit)+1, "退出 AppForce", true)
	position := point{int32(int16(location)), int32(int16(location >> 16))}
	if position.X == -1 && position.Y == -1 {
		getCursorPos.Call(uintptr(unsafe.Pointer(&position)))
	}
	setForegroundWindow.Call(uintptr(window))
	// TPM_RETURNCMD | TPM_NONOTIFY | TPM_RIGHTBUTTON returns the action directly.
	id, _, _ := trackPopupMenu.Call(menu, 0x0182, uintptr(position.X), uintptr(position.Y), uintptr(window), 0)
	postMessage.Call(uintptr(window), 0, 0, 0) // WM_NULL lets the Shell dismiss the popup correctly.
	if id >= 1 && id <= uintptr(Exit)+1 {
		t.dispatch(Action(id - 1))
	}
}
