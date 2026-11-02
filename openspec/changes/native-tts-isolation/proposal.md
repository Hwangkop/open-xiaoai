# Proposal

## Why

原生唤醒接管已经能把「小爱同学」这一轮识别出的文本交给豆包，但小爱自己的 TTS 仍会漏出来：一方面 `mico_aivs_lab` 在 final 之后可能已经把 TTS 请求发进 `mediaplayer` 的 `mibrain` 队列；另一方面接管原来在**每一句**豆包 TTS 播完就解冻，第一句结束后 aivs 就被放行，后续句子播放时小爱可以插话。用户的目标很明确：**可以劫持小爱 ASR，但不要播放小爱 TTS；同时音乐尽量继续播放**。

前期已经讨论、并在设备上验证过多条路线（冻播放器、冻 aivs、停 mibrain 队列、本地预冻结、wav/aplay、mp3/miplayer、劫持 TTS URL、网络层拦截），也发现豆包 TTS 与音乐的音量目前不能独立控制。现在需要把这些讨论固化成可执行的实验与验收标准，逐项实测后再定最终方案，避免凭感觉选型或漏测。

## What Changes

- 新增「原生 TTS 隔离」：接管开始时清空 `mediaplayer` 的 `media:"mibrain"` TTS 队列；冻结 `mico_aivs_lab` 作为防止继续生成 TTS 的兜底；接管生命周期覆盖**整轮回复**，不再逐句解冻。
- 客户端在 `instruction.log` 出现终态识别时用 inotify 低延迟唤醒，本地先 `SIGSTOP aivs`，服务端再决定接管或释放；带超时自动解冻兜底。
- 明确豆包 TTS 的目标播放路径：`wav → aplay -D notify`（与音乐 dmix 共存）优先；`mp3 → miplayer`（会暂停音乐）作为兼容路径。真实 TTS 的两条路径都要实测并记录结论，当前 WAV 默认已走 notify。
- 新增豆包 TTS 与音乐音量的独立控制设计：方案 A 使用 `tts.volume → notifyvol` 配置与 MCP 工具；方案 B（服务端 TTS 增益/豆包 volume 参数）作为备选。真机已确认 `mysoftvol` 会改变音乐听感，即使 mediaplayer 报告的 volume 不变，因此保持固定、不得作为 TTS 音量旋钮；`notify + notifyvol` 短音测试已确认响度变化且音乐听感不变，真实豆包 TTS 待验收。
- 建立实验矩阵：每个候选方案都必须有明确的测试步骤、观察指标、结论和保留/淘汰原因；URL 劫持（LD_PRELOAD）与网络层拦截也必须给出可行性结论。
- 新增当前固件的实验性 SDK 指令隔离：按 dialog 在执行前拦截 TTS/播放/音量动作；播放器唤醒焦点单独受控过滤，带短租约和回滚。经用户要求继续真机探索，已备份并临时部署，未合并 main。
- 指令隔离支持显式启用开机加载：在 client 连接前核验已验证固件与载荷指纹，重建临时覆盖；失败关闭持久开关并兼容启动，支持持久禁用回滚。
- **不做**：不引入 Docker；不覆盖 main 的稳定冻结方案；不把实验部署等同验收。

## Capabilities

### New Capabilities

- `native-tts-isolation`: 定义原生唤醒接管期间“小爱 TTS 不得出声、音乐继续、接管覆盖整轮回复、候选方案必须实测”的行为契约。
- `audio-output-routing`: 定义音乐、原生 TTS、豆包 TTS 各自使用哪条播放通道，以及 TTS 与音乐音量如何独立控制。

### Modified Capabilities

- `voice-session-state`: 修改“播报不打断音乐态”等要求，补充原生接管期间 `mediaplayer status=0` 视为中断而非播完、接管生命周期由会话状态所有者按整轮回复管理、TTS 音量与音乐音量独立。

## Impact

- 服务端：`apps/chat/engine.go`、`speaker.go`、`speaker_native_takeover.go`、`runtime.go`、`server.go`、`mcp.go`、音量相关配置。
- 客户端：`apps/client/cmd/client/main.go`、`apps/client/services/monitor/*`（inotify、本地预冻结、探针）。
- 设备：`mediaplayer` 的 `media` 通道（`common` / `mibrain`）、`notifyvol`、`aplay -D notify`、`miplayer`；`mysoftvol` 保持固定。
- 可能的后续实验：`LD_PRELOAD` 钩 `ubus_invoke`（需要 ARM 交叉工具链）；网络层拦截（预计淘汰）。
- 文档：`docs/device-audio-aec-native-asr.md` 第 13 节音量分层、第 14 节与部署/复测说明。
