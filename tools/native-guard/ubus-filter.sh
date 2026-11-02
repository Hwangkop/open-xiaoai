#!/bin/sh
# 仅过滤固件的播放器唤醒焦点，所有其他命令完整委托原二进制。
native_guard_focus_call() {
    if [ "${1:-}" = -t ]; then shift 2; fi
    [ "$#" = 4 ] && [ "$1" = call ] && [ "$2" = mediaplayer ] && [ "$3" = player_wakeup ] || return 1
    case "$4" in
        '{"action":"start"}'|'{"action":"stop"}'|'{"action":"multistart"}') ;;
        *) return 1 ;;
    esac
    guard_policy=/tmp/xiaoai-native-guard/policy
    guard_valid=0
    if [ -r "$guard_policy" ]; then
        {
            read -r guard_version
            read -r guard_owner
            read -r guard_expiry
        } < "$guard_policy"
        case "$guard_owner:$guard_expiry" in
            *[!0-9:]*|:|:*|*:) ;;
            *)
                guard_now=$(date +%s)
                if [ "$guard_version" = 1 ] && [ "$guard_owner" -gt 1 ] &&
                   [ "$guard_expiry" -gt "$guard_now" ] && [ "$guard_expiry" -le "$((guard_now + 20))" ] &&
                   kill -0 "$guard_owner" 2>/dev/null &&
                   [ "$(cat /tmp/xiaoai-native-guard/ready 2>/dev/null)" = "$(pidof mico_aivs_lab)" ]; then
                    guard_valid=1
                fi
                ;;
        esac
    fi
    [ "$guard_valid" = 1 ]
}
if native_guard_focus_call "$@"; then
    echo "🛡️ 跳过播放器唤醒焦点 time=$(date +%s)" >> /tmp/xiaoai-native-guard/events.log
    echo '{"code":0}'
    exit 0
fi
exec /data/open-xiaoai/ubus.native-guard.original "$@"
