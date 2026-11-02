# Spec Delta

## Purpose

定义音乐、原生小爱 TTS、豆包 TTS 各自使用哪条播放通道，以及豆包 TTS 在什么条件下能与音乐共存，避免把三者的播放路径混在一起。

## ADDED Requirements

### Requirement: 播放通道分工

音乐 SHALL 走 `mediaplayer` 的 `common` 通道；原生小爱 TTS SHALL 走 `mediaplayer` 的 `mibrain` 通道；豆包 TTS SHALL 使用不经过 `mibrain` 的独立通道。清空 `mibrain` 队列 MUST NOT 停止 `common` 上的音乐。

#### Scenario: 三种声音的通道

- **WHEN** 系统同时拥有音乐、原生 TTS、豆包 TTS 三种声源
- **THEN** 音乐在 common，原生 TTS 在 mibrain，豆包 TTS 走独立通道

#### Scenario: 清空原生 TTS

- **WHEN** 系统对 `mibrain` 执行 stop
- **THEN** 原生 TTS 队列被清空
- **AND** common 上的音乐继续播放

### Requirement: 豆包 TTS 播放路径选择

系统 SHALL 按配置和音频格式选择豆包 TTS 的播放路径：`wav` MUST 使用 `aplay -D notify`，`mp3` MAY 使用 `miplayer`。选择 MUST 是显式可配置的，MUST NOT 仅靠 URL 后缀猜测。

#### Scenario: wav 走 aplay

- **WHEN** 豆包 TTS 配置为 wav 且音乐正在播放
- **THEN** 系统使用 `aplay -D notify` 播放
- **AND** 播放前不暂停音乐

#### Scenario: mp3 走 miplayer

- **WHEN** 豆包 TTS 配置为 mp3 且音乐正在播放
- **THEN** 系统使用 miplayer 播放，并在播放前暂停音乐、播放后恢复

#### Scenario: 配置与文件格式不一致

- **WHEN** 配置为 wav 但实际音频不是 wav
- **THEN** 播放失败被记录，系统回退到可用的播放路径或报告错误

### Requirement: TTS 使用独立音量控制

豆包 WAV TTS 的 `tts.volume` SHALL 映射到 `notify` PCM 的 `notifyvol`（0–255），音乐音量 SHALL 使用 `mediaplayer volume`。`mysoftvol` 会影响音乐，系统 MUST NOT 将其用于调节 TTS 音量；调整 TTS、音乐音量或执行音乐 ducking 时 SHALL 保持 `mysoftvol` 固定。`notifyvol` 是通道级控制，使用同一 `notify` PCM 的其他声音也可能受其影响。

#### Scenario: 调节 notify 通道音量

- **WHEN** 音乐播放期间调整豆包 WAV TTS 音量
- **THEN** 系统只调整 `notifyvol`，并通过 `aplay -D notify` 播放
- **AND** `mysoftvol` 和音乐听感响度保持不变

#### Scenario: 验证音量隔离

- **WHEN** 使用相同真实豆包 WAV，在 `notifyvol=60` 和 `180` 下分别播放
- **THEN** 人耳确认 TTS 响度变化、音乐听感响度不变
- **AND** 记录音乐播放状态与 mixer 实际值，不仅依赖 mediaplayer 的 volume 字段

### Requirement: 播放路径必须实测

`wav + aplay` 和 `mp3 + miplayer` 两条路径 SHALL 分别实测，记录音乐是否继续、首声延迟、音量表现和失败场景；只有实测通过的路径才能作为默认路径。

#### Scenario: wav 路径实测

- **WHEN** 音乐播放中触发豆包 TTS，配置为 wav
- **THEN** 记录音乐状态、是否听到 TTS、首声延迟

#### Scenario: mp3 路径实测

- **WHEN** 音乐播放中触发豆包 TTS，配置为 mp3
- **THEN** 记录音乐是否被暂停、恢复位置是否正确、首声延迟

#### Scenario: 默认路径选型

- **WHEN** 两条路径都完成实测
- **THEN** 默认路径从实测通过的路径中选择，并记录选择理由
