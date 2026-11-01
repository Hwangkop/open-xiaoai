# Spec Delta

## ADDED Requirements

### Requirement: 播报不打断音乐态

播报期间系统 MUST NOT 把设备的静默当作"音乐结束"；播报结束后 SHALL 恢复音乐播放（设备暂停则 resume，设备已空闲则重新拉起当前曲目）。

#### Scenario: 播报期间不退出音乐态

- **WHEN** 音乐播放中触发一次 TTS 播报
- **THEN** 音乐态保持，音量不还原

#### Scenario: 播报结束恢复播放

- **WHEN** 播报结束且当前仍在音乐态
- **THEN** 音乐继续播放（必要的话重新拉起当前曲目）

### Requirement: 播报回声不回流

播报刚结束时 MUST NOT 把麦克风收到的自身尾音当作用户输入；系统 SHALL 在播报结束后保留一段抑制窗口。

#### Scenario: 确认语不被二次识别

- **WHEN** 点歌确认语"正在播放《X》"播完
- **THEN** 该句不会被当成用户指令再次进入路由
