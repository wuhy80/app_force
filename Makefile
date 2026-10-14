TAGS = with_quic,with_utls,with_gvisor

.DEFAULT_GOAL := help

.PHONY: help frontend build desktop windows test check clean
help: ## 显示命令列表和三平台编译示例（默认命令）
	@printf '%s\n' '用法：make <命令>' '' '可用命令：'
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf '%s\n' '' '三平台编译示例（macOS/Linux 终端）：' \
		'  make windows                                  Windows x64 桌面版' \
		'  make build                                    当前系统的浏览器界面版（macOS/Linux）' \
		'  make desktop                                  当前系统的原生桌面窗口版' \
		'  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build  Linux x64 浏览器界面版（可交叉编译）' \
		'  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 make build  Linux ARM64 浏览器界面版（可交叉编译）' \
		'' '说明：' \
		'  构建需要 Go >= 1.25.5、Node.js >= 22.12；构建命令会自动安装依赖并构建前端。' \
		'  macOS 原生窗口版请在 macOS 上编译；Linux 原生窗口版需要 GTK/WebKit 开发依赖。' \
		'  make build 始终输出 bin/appforce，编译不同平台前请保存已有产物。' \
		'  Windows 本机 PowerShell 构建：powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1'

frontend: ## 安装前端依赖并构建界面
	npm --prefix frontend ci
	npm --prefix frontend run build

build: frontend ## 编译当前系统的浏览器界面版，输出 bin/appforce
	go build -trimpath -tags $(TAGS) -o bin/appforce ./cmd/appforce

desktop: frontend ## 编译当前系统的原生桌面窗口版，输出 bin/appforce-desktop
	go build -trimpath -tags desktop,production,$(TAGS) -o bin/appforce-desktop ./cmd/appforce

windows: frontend ## 交叉编译 Windows x64 桌面版，输出 bin/AppForce.exe
	go run ./build/windows
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags desktop,production,$(TAGS) -ldflags "-H windowsgui" -o bin/AppForce.exe ./cmd/appforce

test: ## 运行 Go 测试并启用竞态检测
	go test -race -tags $(TAGS) ./...

check: ## 执行 Go 静态检查和前端构建检查
	go vet -tags $(TAGS) ./...
	npm --prefix frontend run build

clean: ## 删除 bin 构建产物和生成的 Windows 资源文件
	rm -rf bin
	rm -f cmd/appforce/rsrc_windows_*.syso
