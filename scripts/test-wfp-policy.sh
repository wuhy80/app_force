#!/usr/bin/env bash
set -euo pipefail
task_repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
task_output="$(mktemp -d)"
trap 'rm -rf "$task_output"' EXIT
"${CC:-cc}" -std=c11 -fshort-wchar -Wall -Wextra -Werror \
  "$task_repo/drivers/windows/wfp/tests/policy_test.c" -o "$task_output/policy-test"
"$task_output/policy-test"
