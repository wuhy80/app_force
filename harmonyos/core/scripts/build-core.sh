#!/usr/bin/env bash
set -euo pipefail

task_root="$(cd "$(dirname "$0")/../../.." && pwd)"
task_mode="${1:-ohos}"
task_go="${OHOS_GO:-go}"
task_tags="with_quic,with_utls,with_gvisor"

if [[ "$task_mode" == "--host" ]]; then
  mkdir -p "$task_root/harmonyos/core/build/host"
  cd "$task_root"
  CGO_ENABLED=1 "$task_go" build -trimpath -tags "$task_tags" -buildmode=c-shared \
    -o "$task_root/harmonyos/core/build/host/libappforce_core.so" ./harmonyos/core/cshared
  echo "宿主机共享库已生成，仅用于接口验证，不能安装到鸿蒙手机。"
  exit 0
fi

if [[ "$task_mode" != "ohos" && "$task_mode" != "--check" ]]; then
  echo "用法: bash harmonyos/core/scripts/build-core.sh [--host|--check]" >&2
  exit 2
fi

task_native="${OHOS_NATIVE_HOME:-}"
task_goos="${OHOS_GOOS:-openharmony}"
task_arch="arm64"

if ! command -v "$task_go" >/dev/null 2>&1; then
  echo "未找到 Go 工具链，请设置 OHOS_GO。" >&2; exit 1
fi
task_version="$(GOTOOLCHAIN=local "$task_go" env GOVERSION)"
if [[ ! "$task_version" =~ go1\.([0-9]+)\.([0-9]+) ]] || \
   (( BASH_REMATCH[1] < 25 || (BASH_REMATCH[1] == 25 && BASH_REMATCH[2] < 5) )); then
  echo "当前依赖要求 Go >= 1.25.5，工具链版本为 ${task_version}。不能通过降低 go.mod 版本绕过。" >&2; exit 1
fi
if ! GOTOOLCHAIN=local "$task_go" tool dist list | grep -qx "$task_goos/$task_arch"; then
  echo "工具链未提供 ${task_goos}/${task_arch}。请设置 OHOS_GO 为支持鸿蒙且满足版本要求的工具链。" >&2
  echo "如验证其他移植方案，可显式设置 OHOS_GOOS；编译成功仍须验证鸿蒙 ABI、TLS 和真机加载。" >&2
  exit 1
fi
if [[ -z "$task_native" || ! -x "$task_native/llvm/bin/clang" || ! -d "$task_native/sysroot" ]]; then
  echo "请设置 OHOS_NATIVE_HOME 为 SDK 的 native 目录，需包含 llvm/bin/clang 和 sysroot。" >&2; exit 1
fi
if [[ "$task_mode" == "--check" ]]; then
  echo "Go 版本、目标平台和原生 SDK 路径预检通过；尚未验证真机兼容性。"; exit 0
fi

task_out="$task_root/harmonyos/core/build/ohos"
mkdir -p "$task_out" "$task_root/harmonyos/entry/libs/arm64-v8a"
cat > "$task_out/cc" <<'COMPILER'
#!/usr/bin/env bash
set -euo pipefail
exec "$OHOS_NATIVE_HOME/llvm/bin/clang" --target=aarch64-linux-ohos --sysroot="$OHOS_NATIVE_HOME/sysroot" "$@"
COMPILER
chmod +x "$task_out/cc"
cd "$task_root"
OHOS_NATIVE_HOME="$task_native" GOTOOLCHAIN=local GOOS="$task_goos" GOARCH="$task_arch" CGO_ENABLED=1 \
  CC="$task_out/cc" "$task_go" build -trimpath -tags "$task_tags" -buildmode=c-shared \
  -o "$task_out/libappforce_core.so" ./harmonyos/core/cshared
cp "$task_out/libappforce_core.so" "$task_root/harmonyos/entry/libs/arm64-v8a/libappforce_core.so"
echo "共享库已放入 entry/libs/arm64-v8a；下一步必须执行 HAP 构建和真机加载验证。"
