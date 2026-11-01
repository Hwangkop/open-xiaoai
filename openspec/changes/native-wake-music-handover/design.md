# Design

## Context

见 `proposal.md` 的 Why。实现依赖以下已确认事实（1.62.2 实机）：

- 原生 KWS 与 AEC 在 `mips`/`mipns` 内完成，音乐大声时「小爱同学」仍能唤醒。
- 原生云 ASR 的最终文本写入 `/tmp/mico_aivs_lab/instruction.log`，客户端已经在 tail 该文件并上报服务端；`handleInstruction` 已能解析 `RecognizeResult` 与 `Speak`。
- 实测：在非空 final 到达时 `kill -STOP mediaplayer`，原生回答不会出声；`miplayer` 的播报不受影响。
- 唤醒提示音「我在」由 mips 直接写 ALSA，不经过 `mediaplayer`，冻结挡不住。
- 现有播放路径：TTS（blocking）走 `miplayer`；音乐走 `mediaplayer player_play_url`。
- `voice-session-state` 已确立「会话状态单一所有者」，即 `Speaker`。

## Goals / Non-Goals

**Goals:**

- 音乐态下把原生唤醒的整轮对话交给主助手，回答用我们自己的 TTS。
- 冻结/解冻可自愈、幂等，任何异常都不能让音乐永久停住。
- 默认关闭，关闭时行为与变更前完全一致。

**Non-Goals:**

- 不改固件、不做 LD_PRELOAD、不在协议层实现 ASR-only。
- 不静音原生唤醒提示音「我在」。
- 不接管家电类原生指令。

## Decisions

### 1. 用 `SIGSTOP mediaplayer` 冻结，而不是协议层 ASR-only

`mediaplayer` 同时承载原生 TTS 和音乐；冻住它，两者一起停，而我们的 TTS 走独立进程 `miplayer`。实测已验证。

备选：在 `mico_aivs_lab` 注入 `Execution.RequestControl{disabled:[NLP,TTS]}`。1.76.54 可以（JSON 出口 `Event::toJson`），但 1.62.2 的语音事件走 protobuf，`Event::toJson` 只处理 `Settings.*`，注入无效；要做得先逆向 protobuf 上下文，成本远高于收益。备选：直接杀 `mico_aivs_lab`，会连原生 ASR 一起丢掉。

### 2. 触发点是「非空 final」，不是唤醒事件

唤醒事件只说明用户喊了唤醒词，拿不到内容；final 才代表一句话说完。且实测在 final 之后、`Speak` 之前冻结来得及。

触发条件：非空 final + 配置开关打开 + 文本未命中原生关键词。**不要求正在放歌**——不放歌时同样接管，只是没有音乐可恢复。

### 3. 接管状态机由 `Speaker` 持有

```
IDLE ──final──▶ FROZEN ──回答开始──▶ ANSWERING ──播完──▶ RESTORING ──▶ IDLE
                    │                      │
                    └──── 超时/取消 ───────┴──▶ RESTORING
```

- `FROZEN` 起计时，超过 `native_wake.max_seconds`（默认 25s）强制进入 `RESTORING`。
- `RESTORING` 执行一次幂等解冻：`SIGCONT` + 重新同步 `player_get_play_status`，必要时重新拉起当前曲目。
- 状态变化全部经过 `Speaker`，符合 `voice-session-state` 的单一所有者要求。

### 4. 冻结期间暂停音乐状态轮询

`mediaplayer` 被冻住后 `ubus call` 会挂起直到超时，`trackMusic` 的每秒轮询会堆积。冻结期间跳过轮询并保留当前曲目；解冻后重新读一次状态，再决定 resume 还是重新起播。

### 5. 关键词交还：命中白名单不冻结

`native_keywords`（开灯/关灯等）命中时不进入接管：让原生照常执行和播报，避免「冻了音乐但动作已经执行」的割裂。这类指令不涉及我们的回答。

### 6. 结束接管用「重启播放器」清队列，不用「解冻后 stop」

原生回答在冻结期间已经排进 `mediaplayer`；先 `SIGCONT` 再发 `stop`，解冻那一瞬间它就开始出声，stop 晚 100ms 就晚了（实测能听到小爱冒一声）。所以结束接管时直接 `kill -9` 被冻住的播放器，由 procd 拉起干净实例，队列自然没了；放歌的话再重新起播当前曲目。拿不到新 pid 时退回解冻，避免播放器一直停在 `T`。

### 7. 自愈

- 客户端 `freeze_native` 返回失败（进程不存在）时，服务端不进入接管，走原路径。
- 冻结期间服务端每 300ms 复核一次客户端上报的进程状态；发现 `mediaplayer` 已不是停止态就补一次冻结（实验里出现过一次被外部 `SIGCONT`）。
- 服务端重启后状态清零，启动时先做一次「如果存在冻结标记则解冻」。

## Risks / Trade-offs

- **冻结被外部恢复**（procd/其它守护发 `SIGCONT`）→ 冻结窗口内轮询复核并补冻；超过两次失败就放弃接管、走原生。
- **ubus 调用堆积** → 冻结期间停轮询，解冻后统一重同步。
- **原生非媒体动作仍会执行**（如米家开关）→ 靠 `native_keywords` 白名单交还，并在文档里写明。
- **「我在」仍会响** → 已知限制；后续如需静音，用 1.76.54 项目那套 `fopen`/`fread` 缓冲区清零方案单独做变更。
- **用户冻结期间手动操作音乐** → 以解冻后的真实播放状态为准，不强行覆盖用户操作。
- **不放歌时也接管** → 闹钟/提醒这类原生技能会被豆包接走；用 `native_keywords` 白名单兜住设备控制，其余按"豆包优先"处理。

## Migration Plan

1. 客户端先上（新增 RPC，不影响旧逻辑）。
2. 服务端再上，配置保持默认关闭。
3. 灰度打开 `native_wake.takeover_enabled`，用「音乐播放中说小爱同学问天气」验收。
4. 回滚：关闭配置即可，无固件改动、无残留。

## Open Questions

- 无。静音「我在」与 ASR-only 协议注入都作为后续独立变更处理。
