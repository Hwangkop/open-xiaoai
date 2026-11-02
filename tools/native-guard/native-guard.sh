#!/bin/sh
# 持久开关 + 每次开机重建临时覆盖；只对已校验固件生效，失败不阻止 client 启动。
set -u
BASE=${OPEN_XIAOAI_DIR:-/data/open-xiaoai}
ROOT=${OPEN_XIAOAI_NATIVE_ROOT:-}
STATE=$ROOT/tmp/xiaoai-native-guard
INIT=$ROOT/etc/init.d/mico_aivs_lab
UBUS=$ROOT/bin/ubus
MANIFEST=$BASE/native-guard.sha256
ENABLED=$BASE/native-guard.enabled
FAILED=$BASE/native-guard.failed
LOCK=$ROOT/tmp/xiaoai-native-guard-install.lock
PATCHED=$ROOT/tmp/xiaoai-native-guard-init.sh
FILTER=$ROOT/tmp/xiaoai-native-guard-ubus.sh

log() {
    echo "🛡️ [native-guard] $*"
    mkdir -p "$STATE"
    echo "$(date '+%Y-%m-%d %H:%M:%S') 🛡️ $*" >> "$STATE/boot.log"
}
mounted() { awk -v target="$1" '$2==target {found=1} END {exit !found}' "$ROOT/proc/mounts"; }
expected() { awk -v key="$1" '$2==key {print $1}' "$MANIFEST"; }
verify() {
    [ -f "$2" ] || { log "缺少文件：$2"; return 1; }
    want=$(expected "$1")
    case "$want" in *[!0-9a-f]*|'') log "校验清单条目无效：$1"; return 1;; esac
    [ "${#want}" = 64 ] && [ "$(sha256sum "$2" | awk '{print $1}')" = "$want" ] || {
        log "校验不匹配：$1"; return 1;
    }
}
ready() {
    pid=$(pidof mico_aivs_lab 2>/dev/null || true)
    [ -n "$pid" ] && [ "$pid" = "$(cat "$STATE/ready" 2>/dev/null)" ] &&
        grep -Fq "$BASE/native_guard.so" "$ROOT/proc/$pid/maps"
}
# 验证当前覆盖确为本方案的内容，拒绝卸掉其他人设置的挂载。
patch_init() {
    awk '{print} /procd_set_param command \/usr\/bin\/mico_aivs_lab/ {print "    procd_set_param env LD_PRELOAD=/data/open-xiaoai/native_guard.so"}' "$1"
}
owned_init() {
    [ -f "$BASE/native-guard-init.original" ] || return 1
    patch_init "$BASE/native-guard-init.original" > "$STATE/expected-init" || return 1
    cmp -s "$INIT" "$STATE/expected-init"
}
owned_focus() { cmp -s "$UBUS" "$BASE/native-guard-ubus-filter.sh"; }
remove_policy() {
    [ ! -f "$STATE/ready" ] || rm "$STATE/ready"
    [ ! -f "$STATE/policy" ] || rm "$STATE/policy"
}
restore() {
    remove_policy
    restored=0
    if mounted "$UBUS"; then
        owned_focus || { log '拒绝卸载非本方案的 ubus 覆盖'; return 1; }
        umount "$UBUS" || return 1
    fi
    if mounted "$INIT"; then
        owned_init || { log '拒绝卸载非本方案的 aivs 启动覆盖'; return 1; }
        umount "$INIT" || return 1
        restored=1
    fi
    if [ "$restored" = 1 ]; then
        "$INIT" restart || { log '恢复原生 aivs 启动失败'; return 1; }
    fi
    log '已恢复原生启动与 ubus；client 可继续使用兼容路径'
}
validate() {
    [ -f "$MANIFEST" ] || { log '缺少校验清单'; return 1; }
    verify sdk "$ROOT/usr/lib/libaivs_sdk.so" &&
    verify aivs "$ROOT/usr/bin/mico_aivs_lab" &&
    verify hook "$BASE/native_guard.so" &&
    verify filter "$BASE/native-guard-ubus-filter.sh" || return 1
    if mounted "$INIT"; then
        verify init "$BASE/native-guard-init.original" && owned_init || return 1
    else
        verify init "$INIT" || return 1
    fi
    if mounted "$UBUS"; then
        verify ubus "$BASE/ubus.native-guard.original" && owned_focus || return 1
    else
        verify ubus "$UBUS" || return 1
    fi
}
prepare() {
    [ ! -f "$BASE/speech-host" ] || { log '语音宿主模式与指令隔离互斥'; return 1; }
    validate || return 1
    # 不在挂载时重写原件备份，避免把覆盖文件当原件而形成递归委托。
    if ! mounted "$INIT"; then
        cp "$INIT" "$BASE/native-guard-init.original" || return 1
        patch_init "$INIT" > "$PATCHED.new" || return 1
        chmod 755 "$PATCHED.new" && sh -n "$PATCHED.new" && mv "$PATCHED.new" "$PATCHED" || return 1
        mount -o bind "$PATCHED" "$INIT" || return 1
    fi
    if ! ready; then
        remove_policy
        "$INIT" restart || return 1
        attempt=0
        stable_pid=
        stable_count=0
        # 冷启动时固件可能再次拉起 aivs；等待当前实例稳定，不能首次换 PID 就判失败。
        while [ "$attempt" -lt 30 ]; do
            sleep 1
            attempt=$((attempt + 1))
            if ready; then
                if [ "$pid" = "$stable_pid" ]; then
                    stable_count=$((stable_count + 1))
                else
                    stable_pid=$pid
                    stable_count=0
                    log "等待 aivs 实例稳定：pid=$pid"
                fi
                [ "$stable_count" -ge 3 ] && break
            else
                stable_pid=
                stable_count=0
            fi
        done
        [ "$stable_count" -ge 3 ] && ready && [ "$pid" = "$stable_pid" ] || {
            log '钩子未就绪或 aivs 在 30 秒内未稳定，停止自动加载'; return 1;
        }
    fi
    if ! mounted "$UBUS"; then
        cp "$UBUS" "$BASE/ubus.native-guard.original" || return 1
        chmod 755 "$BASE/ubus.native-guard.original" || return 1
        cp "$BASE/native-guard-ubus-filter.sh" "$FILTER.new" || return 1
        chmod 755 "$FILTER.new" && sh -n "$FILTER.new" && mv "$FILTER.new" "$FILTER" || return 1
        mount -o bind "$FILTER" "$UBUS" || return 1
    fi
    ready && mounted "$INIT" && mounted "$UBUS" || return 1
    log "自动加载核验通过：aivs=${pid}；等待在线 client 租约"
}
fail_open() {
    # 失败熔断写入 /data，避免每次 client 重启都重复尝试加载失败的库。
    [ ! -f "$ENABLED" ] || rm "$ENABLED"
    echo "$(date '+%Y-%m-%d %H:%M:%S') 自动加载失败，已关闭；检查 boot.log 后手动 enable" > "$FAILED"
    log '自动加载失败，已关闭持久开关并回退；不会阻止 client 启动'
    restore || log '部分回退失败，请检查挂载与原生服务'
    return 1
}
status() {
    [ ! -f "$ENABLED" ] || echo '持久开关：已开启'
    [ -f "$ENABLED" ] || echo '持久开关：已关闭'
    if ready && mounted "$INIT" && mounted "$UBUS"; then
        echo "运行状态：指令钩子与焦点过滤均已加载，aivs=$pid"
    else
        echo '运行状态：未完整加载，客户端将按实际能力使用兼容路径'
    fi
    [ ! -f "$FAILED" ] || cat "$FAILED"
}
command=${1:-status}
case "$command" in status) status; exit 0;; boot) [ -f "$ENABLED" ] || exit 0;; enable|disable) ;; *) echo '用法：native-guard.sh enable|boot|disable|status'; exit 2;; esac
mkdir -p "$STATE"
chmod 700 "$STATE"
if ! mkdir "$LOCK" 2>/dev/null; then
    owner=$(cat "$LOCK/pid" 2>/dev/null || true)
    if [ -n "$owner" ] && kill -0 "$owner" 2>/dev/null; then log '初始化正在执行，跳过重复调用'; exit 0; fi
    [ ! -f "$LOCK/pid" ] || rm "$LOCK/pid"
    rmdir "$LOCK" && mkdir "$LOCK" || exit 1
fi
echo "$$" > "$LOCK/pid"
trap 'rm "$LOCK/pid"; rmdir "$LOCK"' EXIT
case "$command" in
    disable)
        [ ! -f "$ENABLED" ] || rm "$ENABLED"
        restore
        ;;
    enable)
        if prepare; then
            echo 1 > "$ENABLED.new" && mv "$ENABLED.new" "$ENABLED" || exit 1
            [ ! -f "$FAILED" ] || rm "$FAILED"
        else fail_open; fi
        ;;
    boot) prepare || fail_open;;
esac
