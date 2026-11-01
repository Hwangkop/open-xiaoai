# Proposal

## Why

现在的「小爱同学 → 豆包」是靠冻结 `mediaplayer` 实现的，能用，但有三个绕不开的代价：

- 原生回答已经排进播放器，结束接管时必须重启播放器，**音乐只能从头再放**；
- 唤醒应答「我在」由固件直接写 ALSA，拦不住；
- 冻结/补冻/解冻是一条时序链，偶尔还会被外部 `SIGCONT` 打断。

同固件（OH2P / 1.62.2）的 [stevenjoezhang/xiaoai-agent](https://github.com/stevenjoezhang/xiaoai-agent) 给了另一条路：**停掉 `mico_aivs_lab`，自己 bind `speech.usock` 接管 mipns 的音频**。原生助手不再有进程去生成回答，也就不存在排队、补播、冻结。用户已明确不需要小爱原生技能，这条路可以走。

## What Changes

- client 新增「语音宿主」模式：在 `/tmp/mico_aivs_lab/usock/speech.usock` 上实现 mipns 的 datagram 协议（`REGISTER` / `PREPARE` / `TRANSMITTING` / `END` 上行，`PREPARE_RESPONSE` / `STOP_CAPTURE` / `EXPECT_SPEECH` 下行），接收**已经过 AEC** 的 16k PCM。
- 启动编排（`init.sh`）：停 `pns` 与 `mico_aivs_lab` → 把 mips 从 opus32 改成 PCM → 把原生 socket 挪走并 bind-mount 保护 → 起语音宿主 → 再起 `mico_aivs_lab`（只提供非语音服务）。
- 唤醒仍由 mips 的 KWS 负责：收到 `WAKEUP` 流与音频后开始一轮对话；ASR 走我们现有的引擎（本地 SenseVoice / 云端），不再依赖小米云。
- 播报仍用 `miplayer`；回答期间音乐只压音量，**不再冻结播放器**，位置可以接着放。
- 原生小爱不再应答；家电控制改由主助手的 Home Assistant 工具承担。
- 保留现有冻结方案作为回退，用配置开关在两种模式间切换，验证通过后再默认切换。

**BREAKING**：开启语音宿主后，小爱原生技能（天气、闹钟、米家）不可用。

## Capabilities

### New Capabilities
- `speech-socket-agent`: 接管 `speech.usock` 后的一整套行为——音频通道接管、唤醒与判停、原生助手隔离、播报衔接与回退恢复。

### Modified Capabilities
（无。现有 `voice-session-state` / `voice-routing` 的行为不变。）

## Impact

- `apps/client`：新增语音宿主模式与 socket 协议实现；`init.sh` 增加接管/回退编排。
- `apps/chat`：唤醒与 ASR 文本来源从 `instruction.log` 改为语音宿主上报；新增模式配置。
- 设备运行时：`mico_aivs_lab` 不再处理语音；`mipns` 以 PCM 模式启动；`speech.usock` 由我们持有。
- 文档：部署、回退、与现有冻结方案的关系。
