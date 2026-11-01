#!/usr/bin/env bash
# 手工切换语音识别引擎（运行中生效，重启后回到 config.yaml 的 asr.provider）
#
# 用法：
#   ./asr-switch.sh status        查看当前用的是哪个
#   ./asr-switch.sh doubao        切到豆包（火山 v3，准确率高）
#   ./asr-switch.sh local         切到本地 sherpa（免费离线，速度快）
#
# 服务地址可用环境变量覆盖：CHAT_ADDR=http://127.0.0.1:4399 ./asr-switch.sh status

set -euo pipefail

ADDR="${CHAT_ADDR:-http://127.0.0.1:4399}"
CMD="${1:-status}"

show() {
  curl -sS -m 5 "$ADDR/admin/api/asr" | python3 -c '
import json,sys
d=json.load(sys.stdin)
if not d.get("enabled"):
    print(d.get("message","服务端唤醒未启用")); raise SystemExit(0)
mark={"doubao":"（火山 v3 整段，准确率高）","doubao_stream":"（火山 v3 流式，边说边出字）","local":"（本地 sherpa，免费离线）"}
cur=d.get("provider")
suffix=mark.get(cur,"")
print("当前: %s %s" % (cur, suffix))
print("可用:", ", ".join(d.get("available",[])))
if d.get("last_error"):
    print("上次错误:", d["last_error"])
'
}

case "$CMD" in
  status)
    show
    ;;
  doubao|doubao_stream|local)
    curl -sS -m 5 -X POST "$ADDR/admin/api/asr" \
      -H 'Content-Type: application/json' -d "{\"provider\":\"$CMD\"}" >/dev/null
    show
    ;;
  *)
    echo "用法: $0 [status|doubao|doubao_stream|local]" >&2
    exit 1
    ;;
esac
