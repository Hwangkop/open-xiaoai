#!/bin/sh
set -eu
if awk '$2=="/bin/ubus" {found=1} END {exit !found}' /proc/mounts; then
    echo 'ubus 已有覆盖，停止安装'; exit 1
fi
cp /bin/ubus /data/open-xiaoai/ubus.native-guard.original
chmod 755 /data/open-xiaoai/ubus.native-guard.original
cp /data/open-xiaoai/native-guard-ubus-filter.sh /tmp/xiaoai-native-guard-ubus.sh
chmod 755 /tmp/xiaoai-native-guard-ubus.sh
sh -n /tmp/xiaoai-native-guard-ubus.sh
# 撤掉前一项不足以覆盖 mipns 直接调用的 wakeup.sh 实验。
if awk '$2=="/bin/wakeup.sh" {found=1} END {exit !found}' /proc/mounts; then
    umount /bin/wakeup.sh
fi
mount -o bind /tmp/xiaoai-native-guard-ubus.sh /bin/ubus
echo '已安装临时 ubus 唤醒焦点过滤；其他调用委托原二进制'
