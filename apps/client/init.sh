#!/bin/sh
# open-xiaoai client launcher（BusyBox 兼容）。
#
#   init.sh [start] [client 参数...]   前台运行守护（单实例 + 崩溃自动拉起）；无参数 = start
#   init.sh daemon  [client 参数...]   后台运行守护
#   init.sh stop | restart | status
#   init.sh upgrade <新二进制>          自检 → 替换 → 重启，失败自动回滚到 client.bak
#
# 参数优先级：命令行 > $WORK_DIR/server.txt > ws://127.0.0.1:4399
# 环境变量：
#   OPEN_XIAOAI_DIR     工作目录（默认 /data/open-xiaoai）
#   OPEN_XIAOAI_RUN     运行态/日志目录（默认 /tmp，避免磨损 /data）
#   OPEN_XIAOAI_STRICT  =1 时 server 不可达则拒绝启动（默认仅警告，client 自带重连）
#   OPEN_XIAOAI_MAX_CRASH  连续快速崩溃多少次后放弃（默认 10）
set -u

WORK_DIR="${OPEN_XIAOAI_DIR:-/data/open-xiaoai}"
CLIENT="$WORK_DIR/client"
RUN="${OPEN_XIAOAI_RUN:-/tmp}"
STATE="$RUN/xiaoai-client"        # 锁目录 + 状态文件
LOG="$RUN/xiaoai-client.log"
MAX_CRASH="${OPEN_XIAOAI_MAX_CRASH:-10}"
SELF="$0"

die() { echo "❌ $*" >&2; exit 1; }
now() { date '+%Y-%m-%d %H:%M:%S'; }
alive() { [ -n "${1:-}" ] && kill -0 "$1" 2>/dev/null; }
rd() { [ -f "$STATE/$1" ] && cat "$STATE/$1" 2>/dev/null; }

# 取最终的 client 参数（写入全局 ARGS）
resolve_args() {
    if [ $# -eq 0 ] && [ -s "$WORK_DIR/server.txt" ]; then
        # shellcheck disable=SC2046
        set -- $(tr -d '\r' <"$WORK_DIR/server.txt")
    fi
    [ $# -eq 0 ] && set -- ws://127.0.0.1:4399
    ARGS="$*"
}

# ---------- AEC 参考通道 ----------
# 固件 AEC 的第 7 个输入平面是播放参考；`Loopback Enable` 默认 Disable 时参考恒为 0，
# AEC 直接跳过，放歌时人声会被音乐盖住。切换必须让 PDM 采集**重新打开**才生效，
# 而开机时 pns 已经在 rc.local 之前起来过，所以这里设置完要把 pns 重启一次。
# /tmp/xiaoai-aec.ready 记录“已经为哪个 mipns pid 准备过”，避免重复重启。
aec_log() {
    echo "$*"
    [ -n "${LOG:-}" ] && echo "[$(now)] $*" >>"$LOG"
}

prepare_aec_reference() {
    mixer=""
    for bin in /usr/bin/amixer /usr/sbin/amixer; do
        [ -x "$bin" ] && { mixer="$bin"; break; }
    done
    if [ -z "$mixer" ] && command -v amixer >/dev/null 2>&1; then
        mixer=amixer
    fi
    [ -n "$mixer" ] || { aec_log "⚠️ 找不到 amixer，跳过 AEC 参考通道" >&2; return 0; }

    prev=$("$mixer" -c 0 sget 'Loopback Enable' 2>/dev/null | sed -n "s/^ *Item0: '\([^']*\)'.*/\1/p")
    case "$prev" in Enable | Disable) ;; *)
        aec_log "⚠️ 找不到 Loopback Enable 控制项，跳过 AEC 参考通道" >&2
        return 0
        ;;
    esac

    pid=$(pidof mipns-xiaomi 2>/dev/null)
    marker=/tmp/xiaoai-aec.ready
    # 只有“控制项已经是 Enable”且“当前 mipns 就是上次处理过的那个”才跳过；
    # 只信 marker 会漏掉外部把 Loopback 改回 Disable 的情况。
    if [ "$prev" = Enable ] && [ -n "$pid" ] && [ -f "$marker" ] && [ "$(cat "$marker" 2>/dev/null)" = "$pid" ]; then
        return 0
    fi

    if [ "$prev" != Enable ]; then
        "$mixer" -c 0 sset 'Loopback Enable' Enable >/dev/null 2>&1
        cur=$("$mixer" -c 0 sget 'Loopback Enable' 2>/dev/null | sed -n "s/^ *Item0: '\([^']*\)'.*/\1/p")
        [ "$cur" = Enable ] || { aec_log "❌ Loopback Enable 设置失败（当前: ${cur:-未知}）" >&2; return 1; }
    fi

    # 采集已经跑着时再切开关不生效，必须重开一次 PDM 采集
    if [ -z "$pid" ]; then
        aec_log "🎚️ AEC 参考通道已开启"
        return 0
    fi
    if [ ! -x /etc/init.d/pns ]; then
        # 没有 init 脚本就没法让采集重开；不写 marker，下次启动再试
        aec_log "⚠️ 找不到 /etc/init.d/pns，参考通道已开但采集未重开" >&2
        return 0
    fi

    before=$pid
    /etc/init.d/pns restart >/dev/null 2>&1
    i=0
    while [ "$i" -lt 10 ]; do
        sleep 0.5 2>/dev/null || sleep 1
        pid=$(pidof mipns-xiaomi 2>/dev/null)
        [ -n "$pid" ] && [ "$pid" != "$before" ] && break
        i=$((i + 1))
    done
    if [ -n "$pid" ] && [ "$pid" != "$before" ]; then
        echo "$pid" >"$marker"
        aec_log "🎚️ AEC 参考通道已开启（已重启 pns 重开采集，mipns=${pid}）"
    else
        # restart 失败或没换 pid 时不写 marker，下次启动会重试
        aec_log "⚠️ pns 重启后 mipns 没有换 pid（当前: ${pid:-无}），AEC 可能没重开" >&2
    fi
    return 0
}


# ---------- 语音宿主模式 ----------
# 存在 $WORK_DIR/speech-host 时启用：停掉原生助手，由 client 接管 speech.usock。
# client 起来并绑好 socket 之后，再启动 pns（mips 连上来）和 mico（只留非语音服务）。
SPEECH_HOST_MARKER="$WORK_DIR/speech-host"
SPEECH_SOCKET=/tmp/mico_aivs_lab/usock/speech.usock
SPEECH_SOCKET_BAK=/tmp/speech.usock.native
SPEECH_HOST_PNS=/tmp/pns.pcm

speech_host_enabled() { [ -f "$SPEECH_HOST_MARKER" ]; }

prepare_speech_host() {
    speech_host_enabled || return 0
    echo "🎙️ 语音宿主模式：接管 speech.usock"
    /etc/init.d/mico_aivs_lab stop >/dev/null 2>&1
    /etc/init.d/pns stop >/dev/null 2>&1
    killall mipns-xiaomi 2>/dev/null
    sleep 1
    [ -S "$SPEECH_SOCKET" ] && mv "$SPEECH_SOCKET" "$SPEECH_SOCKET_BAK"
    sed 's/[[:space:]]-r[[:space:]]opus32//g' /etc/init.d/pns > "$SPEECH_HOST_PNS" || return 1
    if grep -q -- '-r[[:space:]]*opus32' "$SPEECH_HOST_PNS"; then
        echo "❌ pns 参数补丁失败（找不到 opus32）" >&2
        return 1
    fi
    chmod +x "$SPEECH_HOST_PNS"
    mount -o bind "$SPEECH_HOST_PNS" /etc/init.d/pns || return 1
    amixer -c 0 sset 'Loopback Enable' Enable >/dev/null 2>&1
    (
        i=0
        while [ "$i" -lt 50 ]; do
            [ -S "$SPEECH_SOCKET" ] && break
            sleep 0.2 2>/dev/null || sleep 1
            i=$((i + 1))
        done
        if [ ! -S "$SPEECH_SOCKET" ]; then
            echo "❌ 语音 socket 没有出现，放弃宿主模式" >&2
            exit 1
        fi
        # 保护 socket：mico 重建时不会把我们的换掉
        mount -o bind "$SPEECH_SOCKET" "$SPEECH_SOCKET" 2>/dev/null
        /etc/init.d/pns start >/dev/null 2>&1
        sleep 2
        /etc/init.d/mico_aivs_lab start >/dev/null 2>&1
        # 重启播放器：mips 重开采集后，旧的 mediaplayer 会丢掉声卡句柄，
        # 表现是"状态显示在播、但一点声音都没有"。
        /etc/init.d/mediaplayer restart >/dev/null 2>&1
        sleep 1
        echo "🎙️ 语音宿主就绪：mipns=$(pidof mipns-xiaomi) aivs=$(pidof mico_aivs_lab) player=$(pidof mediaplayer)"
    ) >>"$LOG" 2>&1 &
    return 0
}

restore_speech_host() {
    speech_host_enabled || return 0
    /etc/init.d/pns stop >/dev/null 2>&1
    umount "$SPEECH_SOCKET" 2>/dev/null
    umount /etc/init.d/pns 2>/dev/null
    rm -f "$SPEECH_SOCKET"
    [ -e "$SPEECH_SOCKET_BAK" ] && mv "$SPEECH_SOCKET_BAK" "$SPEECH_SOCKET"
    /etc/init.d/mico_aivs_lab start >/dev/null 2>&1
    /etc/init.d/pns start >/dev/null 2>&1
    echo "🎙️ 已恢复原生语音服务"
}

# ---------- 预检 ----------
preflight() {
    [ -f "$CLIENT" ] || die "预检失败[二进制]: 找不到 ${CLIENT}，请先下载 client"
    [ -x "$CLIENT" ] || die "预检失败[二进制]: $CLIENT 不可执行，请 chmod +x"
    ok=0; n=0
    skip_next=0
    for a in "$@"; do
        # 带值开关的下一个 token 是它的参数，不能当地址校验
        if [ "$skip_next" = 1 ]; then skip_next=0; continue; fi
        case "$a" in
        -capture-backend | -switch-keywords) skip_next=1; continue ;;
        -capture-backend=* | -switch-keywords=*) continue ;;
        -*) continue ;;
        ws://* | wss://*) ;;
        *) die "预检失败[地址格式]: '$a' 不是 ws:// 或 wss:// 地址" ;;
        esac
        n=$((n + 1))
        hp="${a#*://}"; hp="${hp%%/*}"; hp="${hp%%\?*}"; hp="${hp##*@}"
        host="${hp%%:*}"
        port="${hp##*:}"
        [ "$port" = "$hp" ] && { port=80; case "$a" in wss://*) port=443 ;; esac; }
        [ -n "$host" ] || die "预检失败[地址格式]: '$a' 缺少主机名"
        case "$port" in '' | *[!0-9]*) die "预检失败[地址格式]: '$a' 端口无效" ;; esac
        if probe "$host" "$port"; then ok=1; else echo "⚠️ 预检[server 不可达]: $host:$port" >&2; fi
    done
    [ "$n" -gt 0 ] || die "预检失败[地址格式]: 没有 server 地址"
    if [ "$ok" -eq 0 ] && [ "$PROBE_TOOL" != none ]; then
        [ "${OPEN_XIAOAI_STRICT:-0}" = 1 ] && die "预检失败[server 不可达]: 所有地址 TCP 探活均失败（网络不通或服务端未启动）"
        echo "⚠️ 所有 server 均不可达，仍将启动，client 会自动重连" >&2
    fi
}

PROBE_TOOL=none
command -v nc >/dev/null 2>&1 && PROBE_TOOL=nc
[ "$PROBE_TOOL" = none ] && command -v wget >/dev/null 2>&1 && PROBE_TOOL=wget
# with_timeout SECS CMD...：stdin 置空；超时视为失败（BusyBox 不一定有 timeout）
with_timeout() {
    secs=$1; shift
    "$@" </dev/null >/dev/null 2>&1 &
    p=$!
    i=0
    while [ "$i" -lt $((secs * 5)) ]; do
        if ! kill -0 "$p" 2>/dev/null; then wait "$p"; return $?; fi
        sleep 0.2 2>/dev/null || sleep 1
        i=$((i + 1))
    done
    kill "$p" 2>/dev/null
    wait "$p" 2>/dev/null
    return 1
}
probe() {
    case "$PROBE_TOOL" in
    # BusyBox 1.27 的 nc 只支持 `nc IP PORT`（无 -w/-z），自行用后台 + kill 实现 3 秒超时
    nc) with_timeout 3 nc "$1" "$2" ;;
    # wget 对 WebSocket 端口会得到 HTTP 错误但 TCP 能连上：只要不是连接失败/超时即可
    wget) out=$(wget -T 3 -t 1 -q -O /dev/null "http://$1:$2/" 2>&1); ! echo "$out" | grep -qi "refused\|timed out\|unreachable\|bad address\|can't connect\|resolve" ;;
    *) return 0 ;;
    esac
}

# ---------- 锁 / 状态 ----------
acquire_lock() {
    if mkdir "$STATE" 2>/dev/null; then :; else
        old=$(rd supervisor.pid)
        if alive "$old"; then
            die "已有实例在运行 (pid=$old)，先执行 $SELF stop 或 status"
        fi
        rm -rf "$STATE"
        mkdir "$STATE" 2>/dev/null || die "无法创建锁目录 $STATE"
    fi
    echo $$ >"$STATE/supervisor.pid"
    echo 0 >"$STATE/restarts"
    echo "$ARGS" >"$STATE/args"
    date +%s >"$STATE/started"
}

# ---------- 守护 ----------
supervise() {
    child=""
    trap 'trap - TERM INT; [ -n "$child" ] && kill "$child" 2>/dev/null; echo "stopped (signal) $(now)" >"$STATE/last_exit"; rm -f "$STATE/supervisor.pid" "$STATE/client.pid"; rmdir "$STATE" 2>/dev/null; exit 0' TERM INT
    crashes=0
    while :; do
        # 日志超过 ~1MB 就截断，避免 /tmp 撑满
        [ -f "$LOG" ] && [ "$(wc -c <"$LOG")" -gt 1048576 ] && : >"$LOG"
        echo "[$(now)] 🔥 启动 client: $ARGS" >>"$LOG"
        t0=$(date +%s)
        # shellcheck disable=SC2086
        "$CLIENT" $ARGS >>"$LOG" 2>&1 &
        child=$!
        echo "$child" >"$STATE/client.pid"
        wait "$child"
        code=$?
        child=""
        rm -f "$STATE/client.pid"
        last=$(tail -n 1 "$LOG" 2>/dev/null)
        echo "exit=$code $(now) last_log: $last" >"$STATE/last_exit"
        echo "[$(now)] ⚠️ client 退出 code=$code" >>"$LOG"
        t1=$(date +%s)
        if [ $((t1 - t0)) -lt 30 ]; then crashes=$((crashes + 1)); else crashes=0; fi
        if [ "$crashes" -ge "$MAX_CRASH" ]; then
            msg="‼️ client 连续快速崩溃 $crashes 次，守护放弃。请检查日志: $LOG"
            echo "[$(now)] $msg" | tee -a "$LOG" >&2
            echo "gave-up exit=$code $(now)" >"$STATE/last_exit"
            rm -f "$STATE/supervisor.pid"
            exit 1
        fi
        echo $(($(rd restarts) + 1)) >"$STATE/restarts"
        sleep 5 &
        wait $!
    done
}

cmd_start() {
    resolve_args "$@"
    # shellcheck disable=SC2086
    preflight $ARGS
    acquire_lock
    # 放在取锁之后：已经有一个实例在跑时不要白重启一次 pns。
    # 参考通道没开时 AEC 等于不工作，但失败不该拦住 client 启动（还有 ducking 兜底）。
    prepare_aec_reference || echo "⚠️ AEC 参考通道准备失败，继续启动" >&2
    # 原生指令隔离在 client 连接前重建覆盖；已加载时幂等，失败回退而不拦启动。
    if ! speech_host_enabled && [ -x "$WORK_DIR/native-guard.sh" ]; then
        "$WORK_DIR/native-guard.sh" boot >>"$LOG" 2>&1 ||
            echo "⚠️ 原生指令隔离自动加载失败，继续兼容启动" >>"$LOG"
    fi
    if speech_host_enabled; then
        prepare_speech_host
        # 必须放在 URL 前面：client 用 flag.Parse，遇到第一个非 flag 参数就停了
        ARGS="-speech-host $ARGS"
    fi
    echo "🔥 启动 client: $ARGS (日志: $LOG)"
    supervise
}

cmd_daemon() {
    resolve_args "$@"
    # shellcheck disable=SC2086
    preflight $ARGS
    old=$(rd supervisor.pid)
    alive "$old" && die "已有实例在运行 (pid=$old)"
    # BusyBox 无 setsid/nohup：用后台子 shell 脱离终端
    "$SELF" start "$@" </dev/null >/dev/null 2>&1 &
    sleep 1
    cmd_status
}

cmd_stop() {
    sp=$(rd supervisor.pid)
    cp=$(rd client.pid)
    if ! alive "$sp" && ! alive "$cp"; then
        # 兜底：清理手动运行的孤儿进程
        pkill -f "$CLIENT" 2>/dev/null
        rm -rf "$STATE"
        echo "stopped"
        return 0
    fi
    alive "$sp" && kill "$sp" 2>/dev/null
    alive "$cp" && kill "$cp" 2>/dev/null
    i=0
    while [ $i -lt 20 ]; do
        alive "$sp" || alive "$cp" || break
        sleep 0.5 2>/dev/null || sleep 1
        i=$((i + 1))
    done
    alive "$cp" && kill -9 "$cp" 2>/dev/null
    alive "$sp" && kill -9 "$sp" 2>/dev/null
    if alive "$sp" || alive "$cp"; then die "停止失败，进程仍存在"; fi
    rm -rf "$STATE"
    restore_speech_host
    echo "stopped"
}

cmd_status() {
    sp=$(rd supervisor.pid)
    cp=$(rd client.pid)
    if alive "$sp" && alive "$cp"; then
        up=$(($(date +%s) - $(rd started)))
        line="pid=$cp  server=$(rd args)  uptime=$((up / 3600))h$((up % 3600 / 60))m  restarts=$(rd restarts)"
        conn=$(tail -n 200 "$LOG" 2>/dev/null | grep "已连接:\|已断开连接\|连接失败\|所有地址均失败" | tail -n 1)
        case "$conn" in
        *已连接:*) echo "running  $line" ;;
        *) echo "degraded $line  (无 server 连接: ${conn:-尚未连接})" ;;
        esac
        return 0
    fi
    if alive "$sp"; then echo "degraded supervisor=$sp  (client 正在重启, restarts=$(rd restarts))"; return 1; fi
    echo "stopped  (last exit: $(rd last_exit))"
    return 3
}

cmd_upgrade() {
    new="${1:-}"; [ -n "$new" ] || die "用法: $SELF upgrade <新二进制路径>"
    [ -f "$new" ] || die "找不到 $new"
    cp "$new" "$CLIENT.new" || die "复制失败"
    chmod +x "$CLIENT.new"
    # 自检：无参数时 client 应打印“请输入服务器地址”后退出，证明二进制能在本机运行
    out=$("$CLIENT.new" 2>&1 </dev/null)
    echo "$out" | grep -q "请输入服务器地址" || { rm -f "$CLIENT.new"; die "自检失败，新二进制无法运行，未替换: $out"; }
    was=0; alive "$(rd supervisor.pid)" && was=1
    saved=$(rd args)
    [ "$was" = 1 ] && cmd_stop
    [ -f "$CLIENT" ] && mv "$CLIENT" "$CLIENT.bak"
    mv "$CLIENT.new" "$CLIENT"
    # shellcheck disable=SC2086
    "$SELF" daemon ${saved:-} >/dev/null 2>&1
    sleep 10
    if cmd_status >/dev/null 2>&1; then
        echo "✅ 升级成功"
        return 0
    fi
    echo "⚠️ 新版本启动失败，回滚" >&2
    "$SELF" stop >/dev/null 2>&1
    if [ -f "$CLIENT.bak" ]; then
        mv -f "$CLIENT.bak" "$CLIENT"
        # shellcheck disable=SC2086
        "$SELF" daemon ${saved:-} >/dev/null 2>&1
    fi
    die "升级失败，已回滚到旧二进制"
}

case "${1:-start}" in
start) [ $# -gt 0 ] && shift; cmd_start "$@" ;;
daemon) shift; cmd_daemon "$@" ;;
stop) cmd_stop ;;
restart) shift; cmd_stop; cmd_start "$@" ;;
status) cmd_status ;;
upgrade) shift; cmd_upgrade "$@" ;;
*) cmd_start "$@" ;; # 兼容旧用法：init.sh ws://... / init.sh -switch ws://...
esac
