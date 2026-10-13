//go:build desktop && windows

package main

import (
	"context"
	"fmt"
	"log"

	"appforce/internal/app"
	"appforce/internal/tray"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type localTrayBackend struct{ *app.App }

func (b localTrayBackend) TrayState() tray.State {
	snapshot := b.Snapshot()
	state := tray.State{Running: snapshot.Engine.Running, Mode: snapshot.Settings.Mode}
	for _, node := range snapshot.Nodes {
		if node.ID == snapshot.SelectedNodeID {
			state.Node = node.Name
			break
		}
	}
	for _, rule := range snapshot.Rules {
		if rule.Enabled {
			state.Rules++
		}
	}
	return state
}

func startDesktopTray(d *Desktop) func() {
	show := func() {
		if d.quitting.Load() {
			return
		}
		wruntime.WindowShow(d.ctx)
	}
	report := func(err error) {
		log.Printf("托盘: %v", err)
		go func() {
			if d.quitting.Load() {
				return
			}
			show()
			_, _ = wruntime.MessageDialog(d.ctx, wruntime.MessageDialogOptions{
				Type: wruntime.ErrorDialog, Title: "AppForce", Message: err.Error(),
			})
		}()
	}
	backend := localTrayBackend{d.app}
	// Native can send early events while its startup returns. This channel gates
	// callbacks until the controller is assigned, without blocking the native pump.
	ready := make(chan struct{})
	var controller *tray.Controller
	native, err := tray.StartNative(tray.View{State: backend.TrayState()}, func(action tray.Action) {
		<-ready
		if controller != nil {
			controller.Submit(action)
		}
	}, func(err error) {
		<-ready
		d.trayReady.Store(false)
		if !d.quitting.Load() {
			if err == nil {
				err = fmt.Errorf("托盘已停止，关闭窗口将退出应用")
			}
			report(err)
		}
	})
	if err != nil {
		close(ready)
		report(fmt.Errorf("托盘启动失败，关闭窗口将退出应用: %w", err))
		return func() {}
	}
	controller = tray.NewController(backend, tray.Hooks{
		Render: native.Update, Show: show, Exit: d.quit, Error: report,
	})
	d.trayReady.Store(true)
	close(ready)
	return func() {
		d.trayReady.Store(false)
		controller.Close()
		native.Close()
	}
}

func desktopBeforeClose(d *Desktop, ctx context.Context) bool {
	if d.quitting.Load() || !d.trayReady.Load() {
		return false
	}
	wruntime.WindowHide(ctx)
	return true
}
