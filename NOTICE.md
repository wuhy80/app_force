# 第三方组件

AppForce 直接链接 sing-box，因此本项目以 GPL-3.0-or-later 分发。分发可执行文件时应提供对应完整源码、许可证及依赖许可证；保留构建脚本和锁定版本。

- **sing-box v1.14.3**：Copyright (C) 2022 by nekohasekai，GPL-3.0-or-later，并包含额外限制：“In addition, no derivative work may use the name or imply association with this application without prior consent.” AppForce 不使用上游产品名作为自身名称，不声明官方关联。原始许可证：https://github.com/SagerNet/sing-box/blob/v1.14.3/LICENSE 。
- **sing-tun / 内嵌 Wintun**：三平台网络接口和 Windows 驱动。保留所选依赖版本中的许可证与嵌入二进制许可说明；Wintun 来源 https://www.wintun.net/ 。
- **Wails v2**：MIT，https://github.com/wailsapp/wails/blob/master/LICENSE 。
- **Vue 3 / Vite / TypeScript**：各自许可证随 npm 包提供。
- **gopsutil**：BSD-3-Clause，https://github.com/shirou/gopsutil/blob/master/LICENSE 。
- **yaml.v3**：MIT / Apache-2.0，https://github.com/go-yaml/yaml/blob/v3/LICENSE 。
- **winres**：MIT，https://github.com/tc-hib/winres/blob/master/LICENSE 。

完整 Go 依赖版本见 `go.mod` 与 `go.sum`；前端依赖版本见 `frontend/package-lock.json`。这些依赖还包含各自的传递依赖，正式发布时应一并生成完整许可清单。
