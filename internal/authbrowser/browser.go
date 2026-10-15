// Package authbrowser opens only the authorization window through an explicit
// proxy. Existing browser windows and the system proxy settings are untouched.
package authbrowser

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func Open(address string, dial DialContext) (io.Closer, error) {
	browser := findBrowser()
	if browser == "" {
		return nil, fmt.Errorf("未找到 Edge 或 Chrome，请安装其中一个浏览器后重新打开授权页；也可复制链接到已配置相同代理的浏览器")
	}
	profile, err := os.MkdirTemp("", "appforce-oauth-browser-")
	if err != nil {
		return nil, fmt.Errorf("无法创建授权浏览器的临时目录")
	}
	proxy, err := newProxy(dial)
	if err != nil {
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("无法启动授权浏览器的本机代理")
	}
	cmd := exec.Command(browser, browserArgs(browser, address, profile, proxy.address)...)
	if err := cmd.Start(); err != nil {
		_ = proxy.Close()
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("无法启动授权浏览器，请检查 Edge / Chrome 是否可用")
	}
	go func() {
		_ = cmd.Wait()
		_ = proxy.Close()
		_ = os.RemoveAll(profile)
	}()
	// Keep the success callback visible until the user closes the browser. The
	// manager closes the proxy and its tunnels as soon as authorization ends.
	return proxy, nil
}

func browserArgs(browser, address, profile, proxy string) []string {
	private := "--incognito"
	if strings.Contains(strings.ToLower(browser), "edge") {
		private = "--inprivate"
	}
	return []string{"--user-data-dir=" + profile, private, "--no-first-run", "--no-default-browser-check", "--disable-background-mode", "--proxy-server=http://" + proxy, "--proxy-bypass-list=localhost;127.0.0.1;[::1]", "--new-window", address}
}

func findBrowser() string {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"), filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	case "darwin":
		for _, root := range []string{"/Applications", filepath.Join(os.Getenv("HOME"), "Applications")} {
			candidates = append(candidates, filepath.Join(root, "Google Chrome.app", "Contents", "MacOS", "Google Chrome"), filepath.Join(root, "Microsoft Edge.app", "Contents", "MacOS", "Microsoft Edge"), filepath.Join(root, "Chromium.app", "Contents", "MacOS", "Chromium"))
		}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "microsoft-edge", "microsoft-edge-stable", "chromium", "chromium-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, path)
			}
		}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}
