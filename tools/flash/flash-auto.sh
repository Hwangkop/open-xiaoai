#!/usr/bin/env bash
# 一键刷机：持续扫描 U-Boot 烧录模式，抓到后自动完成 delay / switch / system 三步。
# 用法：bash tools/flash/flash-auto.sh
set -u

cd "$(dirname "$0")" || exit 1

FW="$(cd ../.. && pwd)/tools/client-patch/assets/OH2P_1.62.2/root-patched.squashfs"

if [ ! -f "$FW" ]; then
  echo "❌ 固件不存在: $FW"
  exit 1
fi

echo "固件: $FW"
echo "大小: $(stat -f%z "$FW" 2>/dev/null || stat -c%s "$FW") 字节"
echo
echo "⚡ 开始持续扫描（毫秒级、无间隔）。"
echo "👉 现在开始反复开关音箱电源：关掉 → 等灯灭 → 开 → 立刻再关 → …"
echo "   抓到的瞬间会自动开始刷写，中途千万不要断电。"
echo

scans=0
while true; do
  scans=$((scans + 1))
  out=$(./macos/update identify 1 2>&1)
  if echo "$out" | grep -qi "firmware version"; then
    echo
    echo "✅ 第 ${scans} 次扫描捕获到设备："
    echo "$out" | grep -i "firmware version"
    break
  fi
  if [ $((scans % 200)) -eq 0 ]; then
    echo "…已扫描 ${scans} 次，继续（还没抓到，继续开关电源）"
  fi
done

echo
echo "==> 第 1 步：设置启动延时 15 秒"
./flash delay 15

echo
echo "==> 第 2 步：切换到 boot0"
./flash switch boot0

echo
echo "==> 第 3 步：刷入固件（约 1~2 分钟，请勿断电）"
./flash system system0 "$FW"

echo
echo "🎉 流程结束。若上面显示「刷入成功」，现在拔掉数据线和电源，重新上电。"
