#!/bin/sh
# 只移除本实验的租约和启动覆盖；不碰 mediaplayer/pns/音量。
set -eu
if [ -x /data/open-xiaoai/native-guard.sh ]; then
    exec /data/open-xiaoai/native-guard.sh disable
fi
state=/tmp/xiaoai-native-guard
init=/etc/init.d/mico_aivs_lab
if [ -f "$state/policy" ]; then rm "$state/policy"; fi
if awk '$2=="/bin/ubus" {found=1} END {exit !found}' /proc/mounts; then
    umount /bin/ubus
fi
if awk '$2=="/bin/wakeup.sh" {found=1} END {exit !found}' /proc/mounts; then
    umount /bin/wakeup.sh
fi
if awk '$2=="/etc/init.d/mico_aivs_lab" {found=1} END {exit !found}' /proc/mounts; then
    umount "$init"
    "$init" restart
fi
echo '已恢复原生启动方式；服务端还需改回 isolation_mode: freeze'
