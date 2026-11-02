#!/bin/sh
set -eu
cd "$(dirname "$0")"
out="${1:-/tmp/xiaoai-native-guard-build}"
mkdir -p "$out"
zig cc -target arm-linux-gnueabihf.2.25 -shared -fPIC -Os -Wall -Wextra -Werror \
    native_guard.c -ldl -lpthread -o "$out/native_guard.so"
zig cc -target arm-linux-gnueabihf.2.25 -Os -Wall -Wextra -Werror -Wl,--export-dynamic -Wl,--no-as-needed \
    test_guard.c -ldl -lpthread -o "$out/test_guard"
