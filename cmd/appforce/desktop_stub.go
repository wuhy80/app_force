//go:build !desktop

package main

import (
	"context"
	"log"
	"os/exec"
	"runtime"

	"appforce/internal/app"
)

func runDesktop(ctx context.Context, a *app.App, address string) error {
	// The fragment stays in the browser and is never transmitted in HTTP URLs or logs.
	address += "#token=" + a.Token
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	if err := command.Run(); err != nil {
		log.Printf("无法自动打开浏览器，请手动访问管理地址并输入密钥")
	}
	<-ctx.Done()
	return nil
}
