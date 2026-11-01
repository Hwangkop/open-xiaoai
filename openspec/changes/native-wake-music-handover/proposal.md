# Proposal

## Why

音乐大声时用户喊「小爱同学」能唤醒音箱（原生 KWS + AEC 在 mips 内生效），但整轮对话仍由小爱处理：音乐被它自己压低、回答内容不可控、也无法接我们已有的豆包上下文。豆包链路目前只在「豆包豆豆」这类自有唤醒词下工作，而自有唤醒在高音量下听不见。

实测已经确认一条可行路径：原生 ASR 出 final 时把 `mediaplayer` 冻结（`SIGSTOP`），原生 TTS 与音乐一起停住；我们自己的 TTS 走 `miplayer`，不受影响。因此可以在不逆向协议、不改固件的前提下，把「小爱同学」变成豆包的唤醒入口。

## What Changes

- 新增「原生唤醒接管」：检测到原生 ASR 的非空 final 时，冻结 `mediaplayer`，把文本交给豆包，用 `miplayer` 播报，播完解冻并恢复播放（放歌时恢复音乐）。不限于音乐态。
- 客户端新增 `freeze_native` / `unfreeze_native` 两个 RPC，实现为对 `mediaplayer` 的 `SIGSTOP` / `SIGCONT`；冻结期间暂停音乐状态轮询，避免 ubus 调用堆积。
- 服务端在 `handleInstruction` 里识别非空原生 final，进入接管模式并走主助手；原生家电关键词仍交还小爱，避免两边同时执行。
- 冻结必须可自愈：超时、进程消失、服务端重启都要自动解冻，不能让音乐永久停住。
- 新增配置 `native_wake.takeover_enabled`（默认 `false`），只在音乐态生效，关闭时行为与现在完全一致。

**范围约定**（本次不做）：
- 原生唤醒提示音「我在」由 mips 直接走 ALSA，不经过 `mediaplayer`，本变更不静音它。
- 不放歌时的接管不做额外处理（没有音乐需要恢复）。
- 不接管原生家电指令（开灯/关灯等），仍走 `native_keywords` 交还逻辑。

## Capabilities

### New Capabilities
- `native-wake-handover`: 原生唤醒后的接管行为——触发条件、冻结/恢复设备播放、文本转交、失败回退与配置开关。

### Modified Capabilities
- `voice-session-state`: 会话状态所有者新增「原生播放冻结中」状态；冻结期间的播放静默不得被判定为音乐结束，恢复时必须由所有者统一解冻。

## Impact

- `apps/client/cmd/client/main.go`：新增 `freeze_native` / `unfreeze_native` RPC 与进程存活检查。
- `apps/client`：音乐状态轮询在冻结期间暂停；解冻后重新同步播放状态。
- `apps/chat/engine.go`：`handleInstruction` 增加接管分支与状态机。
- `apps/chat/speaker.go`：封装冻结/解冻调用、超时自愈、与现有 ducking/音乐恢复逻辑的衔接。
- `apps/chat/config.yaml` 与管理台「配置」页：新增 `native_wake.*` 配置项。
- 设备运行时：`mediaplayer` 被 `SIGSTOP`/`SIGCONT`；`miplayer`、mips、aivs 不受影响；无固件改动、无 LD_PRELOAD。
