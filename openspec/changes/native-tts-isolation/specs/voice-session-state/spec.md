# Spec Delta

## MODIFIED Requirements

### Requirement: 播报不打断音乐态

播报期间系统 MUST NOT 把设备的静默当作“音乐结束”；播报结束后 SHALL 恢复音乐播放（设备暂停则 resume，设备已空闲则重新拉起当前曲目）。原生唤醒接管期间，接管 SHALL 覆盖整轮回复，MUST NOT 在单句 TTS 播放结束时解冻；原生对话导致的 `mediaplayer status=0` MUST 视为中断而不是播完，MUST NOT 自动切歌；清空原生 TTS 队列 MUST NOT 停掉 `common` 上的音乐。

#### Scenario: 播报期间不退出音乐态

- **WHEN** 音乐播放中触发一次 TTS 播报
- **THEN** 音乐态保持，音量不还原

#### Scenario: 播报结束恢复播放

- **WHEN** 播报结束且当前仍在音乐态
- **THEN** 音乐继续播放（必要的话重新拉起当前曲目）

#### Scenario: 原生接管多句回复

- **WHEN** 原生唤醒接管期间豆包回复包含多句话
- **THEN** 系统在整轮回复播完前不释放接管

#### Scenario: 原生对话停掉播放器

- **WHEN** 原生对话把 `mediaplayer` 停成 `status=0`
- **THEN** 系统把它视为中断而不是音乐播完，不自动切歌

#### Scenario: 清空原生 TTS 队列

- **WHEN** 系统对 `mibrain` 执行 stop
- **THEN** 音乐态保持，common 上的音乐继续播放

## ADDED Requirements

### Requirement: 原生续听不得再次打断音乐

原生接管的豆包回复结束后，若当前处于音乐态，系统 MUST NOT 自动请求原生续听，SHALL 等待用户再次唤醒。非音乐态自动续听 SHALL 在开麦之前登记 ASR 窗口，并使用匹配 dialog 的终态或有限超时关闭；旧窗口的超时及旧 dialog 的结束事件 MUST NOT 关闭新窗口。

#### Scenario: 回答期间新开始播放音乐

- **WHEN** 接管入场时未播放音乐，但豆包本轮已开始播放歌曲
- **THEN** 收尾依据当前音乐态跳过自动原生续听，不使用入场时的 music 快照决定是否再次开麦

#### Scenario: 非音乐态续听没有有效提问

- **WHEN** 自动续听返回本轮空 final 或结束事件，或超过有限超时
- **THEN** 系统关闭对应 ASR 窗口，不将收音期间的播放器暂停当成自然播完

#### Scenario: 旧窗口超时

- **WHEN** 新一轮 ASR 窗口已打开，旧窗口的超时到达
- **THEN** 新窗口保持有效，不因旧超时恢复音乐或关闭新窗口

### Requirement: 原生接管状态由状态所有者持有

原生唤醒接管的 active、轮次编号和冻结进程 SHALL 由会话状态所有者持有；外部调用方 MUST NOT 直接解冻或结束接管。旧轮次的收尾 MUST NOT 结束新轮次的接管；回合结束 SHALL 由同一所有者按整轮回复完成或超时释放。

#### Scenario: 单句结束不释放

- **WHEN** 豆包回复的第一句播放结束
- **THEN** 接管仍由状态所有者持有，不解冻原生助手

#### Scenario: 旧轮次不结束新轮次

- **WHEN** 第一轮回复未收尾时用户开始了第二轮
- **THEN** 第一轮的收尾不会结束第二轮的接管

#### Scenario: 超时释放

- **WHEN** 接管超过配置上限仍未结束
- **THEN** 状态所有者强制释放接管

### Requirement: TTS 与音乐音量独立

豆包 WAV TTS 与音乐 SHALL 使用独立的音量层：TTS 使用 `aplay -D notify` 和 `notifyvol`，音乐使用 `mediaplayer volume`，`mysoftvol` 保持固定且 MUST NOT 用于 TTS 音量。调整豆包 TTS 音量 MUST NOT 改变音乐听感响度，音乐 ducking MUST NOT 改变豆包 TTS 音量。音量值 SHALL 持久化并在重启后恢复；系统 SHALL 通过 MCP 工具或配置分别控制两者。

#### Scenario: 分别设置音量

- **WHEN** 用户把豆包 TTS 音量设为 50、音乐音量设为 80
- **THEN** TTS 通过 `notifyvol`、音乐通过 `mediaplayer volume` 调节，两个音量互不影响
- **AND** `mysoftvol` 保持固定，人耳确认音乐与 TTS 的隔离效果

#### Scenario: 音乐 ducking

- **WHEN** 播报期间音乐被压到 ducking 音量
- **THEN** 豆包 TTS 的音量保持不变

#### Scenario: 重启后恢复

- **WHEN** 服务重启
- **THEN** 豆包 TTS 与音乐的音量分别恢复到上次设置

#### Scenario: 语音调整音量

- **WHEN** 用户通过语音或 MCP 工具调整豆包 TTS 音量
- **THEN** 只改变 `notifyvol`，音乐听感响度和 `mysoftvol` 保持不变
