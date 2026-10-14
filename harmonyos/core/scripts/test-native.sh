#!/usr/bin/env bash
set -euo pipefail
# Host verification only. Requires Node N-API headers and a C/C++ compiler.
task_root="$(cd "$(dirname "$0")/../../.." && pwd)"
task_headers="${NODE_INCLUDE_DIR:-/usr/local/include/node}"
if [[ ! -f "$task_headers/node_api.h" && -f /usr/include/node/node_api.h ]]; then task_headers=/usr/include/node; fi
if [[ ! -f "$task_headers/node_api.h" ]]; then echo '请设置 NODE_INCLUDE_DIR 为 Node 头文件目录。' >&2; exit 1; fi
task_tmp="$(mktemp -d)"
trap 'rm -rf "$task_tmp"' EXIT
mkdir -p "$task_tmp/napi" "$task_tmp/fixture"
printf '#include <node_api.h>\n' > "$task_tmp/napi/native_api.h"
task_flags=(-shared -fPIC)
if [[ "$(uname -s)" == Darwin ]]; then task_flags+=(-undefined dynamic_lookup); fi
"${CXX:-c++}" -std=c++17 -Wall -Wextra -Werror -Wno-deprecated-declarations "${task_flags[@]}" \
  -I"$task_tmp" -I"$task_headers" "$task_root/harmonyos/entry/src/main/cpp/appforce_napi.cpp" -o "$task_tmp/appforce.node"
bash "$task_root/harmonyos/core/scripts/build-core.sh" --host
DYLD_LIBRARY_PATH="$task_root/harmonyos/core/build/host" LD_LIBRARY_PATH="$task_root/harmonyos/core/build/host" \
  node "$task_root/harmonyos/core/tests/native-smoke.cjs" "$task_tmp/appforce.node"
"${CC:-cc}" -shared -fPIC -Wall -Wextra -Werror "$task_root/harmonyos/core/tests/protect-fixture.c" \
  -o "$task_tmp/fixture/libappforce_core.so"
DYLD_LIBRARY_PATH="$task_tmp/fixture" LD_LIBRARY_PATH="$task_tmp/fixture" \
  node "$task_root/harmonyos/core/tests/protect-smoke.cjs" "$task_tmp/appforce.node"
