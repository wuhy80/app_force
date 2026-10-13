//go:build desktop

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"

	"appforce/internal/api"
	"appforce/internal/app"
	"appforce/internal/web"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type Desktop struct {
	app         *app.App
	ctx         context.Context
	ctxReady    chan struct{}
	handler     http.Handler
	quitting    atomic.Bool
	trayReady   atomic.Bool
	lifecycleMu sync.Mutex
}

type Reply struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// Request routes the desktop interface through the same authenticated API as browser clients.
func (d *Desktop) Request(method, path, body string) (Reply, error) {
	if !strings.HasPrefix(path, "/api/") || strings.Contains(path, "..") {
		return Reply{}, fmt.Errorf("无效请求")
	}
	<-d.ctxReady
	req, err := http.NewRequestWithContext(d.ctx, method, "http://localhost"+path, strings.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+d.app.Token)
	recorder := httptest.NewRecorder()
	d.handler.ServeHTTP(recorder, req)
	return Reply{recorder.Code, recorder.Body.String()}, nil
}

func (d *Desktop) ChooseProgram() (string, error) {
	<-d.ctxReady
	return wruntime.OpenFileDialog(d.ctx, wruntime.OpenDialogOptions{Title: "选择要代理的程序"})
}

func (d *Desktop) quit() {
	if d.quitting.Swap(true) {
		return
	}
	wruntime.Quit(d.ctx)
}

func runDesktop(ctx context.Context, a *app.App, address string) error {
	d := &Desktop{app: a, handler: api.Handler(a), ctxReady: make(chan struct{})}
	var closeTray = func() {}
	return wails.Run(&options.App{
		Title: "AppForce", Width: 1280, Height: 820, MinWidth: 900, MinHeight: 640,
		BackgroundColour: options.NewRGB(247, 248, 250),
		AssetServer:      &assetserver.Options{Assets: web.Assets()}, Bind: []interface{}{d},
		OnStartup: func(wctx context.Context) {
			d.lifecycleMu.Lock()
			defer d.lifecycleMu.Unlock()
			if d.quitting.Load() {
				return
			}
			d.ctx = wctx
			close(d.ctxReady)
			closeTray = startDesktopTray(d)
			go func() {
				select {
				case <-ctx.Done():
					d.quit()
				case <-wctx.Done():
				}
			}()
		},
		OnBeforeClose: func(wctx context.Context) bool { return desktopBeforeClose(d, wctx) },
		OnShutdown: func(context.Context) {
			d.quitting.Store(true)
			d.lifecycleMu.Lock()
			defer d.lifecycleMu.Unlock()
			closeTray()
			_ = a.Stop()
		},
	})
}
