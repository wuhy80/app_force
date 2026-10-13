package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"appforce/internal/api"
	"appforce/internal/app"
	"appforce/internal/security"
	"github.com/gofrs/flock"
)

func main() {
	defaultDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	data := flag.String("data", filepath.Join(defaultDir, "AppForce"), "配置和管理密钥目录")
	listen := flag.String("listen", "127.0.0.1:17891", "管理服务监听地址")
	headless := flag.Bool("headless", false, "仅运行后台和网页管理")
	cert := flag.String("tls-cert", "", "HTTPS 证书 PEM 路径")
	key := flag.String("tls-key", "", "HTTPS 私钥 PEM 路径")
	autostart := flag.Bool("autostart", false, "启动时应用已保存的代理配置")
	showToken := flag.Bool("show-token", false, "显示管理密钥并退出")
	initTLS := flag.Bool("init-tls", false, "生成设备 HTTPS 证书，显示指纹并退出")
	flag.Parse()
	a, err := app.Open(*data)
	if err != nil {
		log.Fatal(err)
	}
	if *showToken {
		fmt.Println(a.Token)
		return
	}
	if *initTLS {
		fingerprint, err := security.GenerateCertificate(*data)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("证书: %s\n私钥: %s\nSHA-256: %s\n", filepath.Join(*data, "server.crt"), filepath.Join(*data, "server.key"), fingerprint)
		return
	}
	lock := flock.New(filepath.Join(*data, "appforce.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		log.Fatal(err)
	}
	if !locked {
		log.Fatal("该配置目录已被另一个 AppForce 实例使用")
	}
	defer lock.Unlock()
	if (*cert == "") != (*key == "") {
		log.Fatal("TLS 证书和私钥必须同时指定")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatal(err)
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && *cert == "" {
		log.Fatal("非回环监听必须配置 --tls-cert 和 --tls-key")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: api.Handler(a), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 65 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer a.Stop()
	go func() {
		var serveErr error
		if *cert != "" {
			serveErr = server.ServeTLS(listener, *cert, *key)
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("管理服务失败: %v", serveErr)
			cancel()
		}
	}()
	if *autostart {
		if err = a.Start(); err != nil {
			log.Printf("自动启动失败，可在界面中调整: %v", err)
		}
	}
	scheme := "http"
	if *cert != "" {
		scheme = "https"
	}
	address := scheme + "://" + listener.Addr().String()
	log.Printf("AppForce 管理地址: %s；密钥文件: %s", address, filepath.Join(*data, "token"))
	if !*headless {
		if err = runDesktop(ctx, a, address); err != nil {
			log.Printf("桌面界面: %v", err)
		}
	} else {
		<-ctx.Done()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
}
