# Design

## Context

当前接管方案（冻结 `mediaplayer`）见 `docs/device-audio-aec-native-asr.md` 第 11 节，代价是音乐位置丢失、「我在」拦不住、依赖冻结时序。

参考实现 [stevenjoezhang/xiaoai-agent](https://github.com/stevenjoezhang/xiaoai-agent) 在**同一固件（OH2P / 1.62.2）**上跑通，做法是停掉 `mico_aivs_lab`、自己 bind `speech.usock`。它的 `start-agent.sh` 负责服务编排，Rust 主程序负责协议。我们只借它的**协议与编排**，用 Go 重写进现有 client。

## Goals / Non-Goals

**Goals:**

- 原生助手完全不参与语音，音乐不再被冻结或重启。
- 唤醒仍用固件 KWS（「小爱同学」），音频用 AEC 后的 PCM。
- 可一键回退到原生服务，也可回退到现有冻结方案。

**Non-Goals:**

- 不保留小爱原生技能（用户已确认不需要）。
- 不实现原生云端 ASR；识别用我们自己的引擎。
- 不在本变更里删除冻结方案（保留开关）。

## Decisions

### 1. 接管 `speech.usock`，而不是冻结播放器

冻结是"在出口堵"，会积累排队的 TTS；接管是"换掉大脑"，原生根本不产生回答。副作用只有一个：原生技能消失。用户已接受。

### 2. 协议：unix datagram，上行/下行各一组消息

从参考实现读到的帧格式与消息编号：

| 方向 | 编号 | 含义 |
|---|---|---|
| 上行 0 | REGISTER(0) | 注册为语音服务 |
| 上行 | STREAM_PREPARE(1) | 请求收音，带 `activate_mode`（0=唤醒、1=追问） |
| 上行 | STREAM_CANCEL(2) / STREAM_END(4) | 取消 / 结束收音 |
| 上行 | STREAM_TRANSMITTING(3) | 音频帧（PCM） |
| 上行 | VOIP(5) / TTS_FINISH(6) / MULTI_CHANNEL(7) | 其它 |
| 下行 1 | REGISTER_RESPONSE(0) | 注册结果 |
| 下行 | STREAM_PREPARE_RESPONSE(1) | 同意收音 |
| 下行 | STOP_CAPTURE(2) / EXPECT_SPEECH(3) / DIALOG_FINISH(5) / ENABLE_VOICE_WAKEUP(9) | 控制 |
| 音频流 | WAKEUP(0) / WAKEUP_END(1) / ASR(2) | 唤醒音频 / 唤醒结束 / 识别音频 |

实现顺序：先只做 `REGISTER` + 收 `WAKEUP`/`ASR` 并打印长度（只读探针），确认 1.62.2 的帧布局与参考实现一致，再做完整对话。

### 3. 启动编排（照抄 `start-agent.sh` 的思路）

```
1. 停 pns（mipns-*）与 mico_aivs_lab
2. 用 bind mount 覆盖 /etc/init.d/pns，把 mipns 参数从 -r opus32 改成 PCM
3. 把原生 speech.usock 改名备份，起语音宿主让它自己 bind
4. mount -o bind speech.usock speech.usock  ← 保护 inode，防 mico 重建时替换
5. 重新起 mico_aivs_lab（只为蓝牙网关等非语音服务），它发现 socket 被占用/保护就不会替换
6. 看护线程每 5 秒检查 inode，变了就重启接管
```

停止时反向执行：停宿主 → 解除保护 → 还原文件 → 起 mico → 起 pns。

### 4. 唤醒与判停

- 唤醒事件来自 `WAKEUP` 流（mips 的 KWS 命中），音频一并送到。
- 收到唤醒后用 `STREAM_PREPARE` 请求 ASR 收音；判停用我们现有的 VAD（静音阈值可配），决定后发 `STREAM_END`。
- 识别走现有 ASR 引擎（本地 SenseVoice / 云端），文本进入现有 `handleInstruction` 等价入口。

### 5. 播报与音乐

- TTS 仍走 `miplayer`，播放器不再冻结。
- 播报前把音乐音量压到 `music_duck.volume`，播完恢复；音乐位置由播放器自己保留。
- 播报期间上行音频做抑制（现有 `markSpeaking` 逻辑），避免把自己的声音识别进去。

### 6. 回退与看护

- 配置 `native_wake.mode: freeze | speech_host`（默认 `freeze`），切换需要重启 client。
- `start-agent.sh` 式的看护：宿主进程退出或 socket inode 变化 → 重启接管；连续失败 3 次 → 回落原生服务并记录。

## Risks / Trade-offs

- **协议细节与参考实现不同** → 先做只读探针，只注册 + 打日志，确认能收到唤醒/ASR 帧再继续。
- **mico 重建 socket 覆盖我们的** → bind-mount 保护 + inode 看护；实测确认 `mico_aivs_lab` 在 socket 被占用时的行为。
- **PCM 模式补丁依赖 init 脚本结构** → 按固件哈希校验，不匹配就拒绝接管并回退。
- **失去原生技能** → 已确认接受；家电走 HA 工具。
- **我们自己的 ASR 在音乐大声时不准** → 用的是 AEC 后的音频，理论上与原生同源；仍需实机验收。

## Migration Plan

1. 只读探针验证协议（不影响现有链路）。
2. 实现宿主 + 开关，默认关闭；灰度打开。
3. 验收：唤醒、识别、播报、音乐连续、回退。
4. 稳定后再把默认模式切到 `speech_host`，冻结方案降级为回退。

## Open Questions

- `mico_aivs_lab` 的 `common.usock` 是否还需要它继续提供（参考实现保留了 mico 进程）。
- `/data/mipns/dialog_continuous`（full-duplex 开关）在 1.62.2 上是否需要一起禁用，待实测。
