#!/bin/sh
# OH2P 1.62.2 临时实验安装：仅对 aivs 注入，重启后 bind mount 自然消失。
set -eu
base=/data/open-xiaoai
state=/tmp/xiaoai-native-guard
original=$base/native-guard-init.original
patched=/tmp/xiaoai-native-guard-init.sh
init=/etc/init.d/mico_aivs_lab
[ "$(id -u)" = 0 ] || { echo '需要 root'; exit 1; }
[ -f "$base/native_guard.so" ] || { echo '缺少 native_guard.so'; exit 1; }
if awk '$2=="/etc/init.d/mico_aivs_lab" {found=1} END {exit !found}' /proc/mounts; then
    echo '已存在临时挂载，先运行 rollback-device.sh'; exit 1
fi
mkdir -p "$state"
chmod 700 "$state"
cp "$init" "$original"
sed '/procd_set_param command \/usr\/bin\/mico_aivs_lab/a\    procd_set_param env LD_PRELOAD=/data/open-xiaoai/native_guard.so' "$original" > "$patched"
chmod 755 "$patched"
sh -n "$patched"
mount -o bind "$patched" "$init"
rollback() {
    echo '钩子启动检查失败，恢复原生启动脚本'
    umount "$init"
    "$init" restart
}
trap rollback EXIT
"$init" restart
n=0
while [ "$n" -lt 15 ]; do
    sleep 1
    pid=$(pidof mico_aivs_lab || true)
    ready=$(cat "$state/ready" 2>/dev/null || true)
    if [ -n "$pid" ] && [ "$pid" = "$ready" ] && grep -q '/data/open-xiaoai/native_guard.so' "/proc/$pid/maps"; then
        sleep 3
        [ "$(pidof mico_aivs_lab || true)" = "$pid" ] || break
        trap - EXIT
        echo "已加载原生指令钩子 pid=$pid（尚未启用认领，等待 client 策略）"
        exit 0
    fi
    n=$((n + 1))
done
exit 1
