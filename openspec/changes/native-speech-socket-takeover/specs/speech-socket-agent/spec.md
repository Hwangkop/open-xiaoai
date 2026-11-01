# Spec Delta

## Purpose

定义「语音宿主」能力：停止原生语音助手，由本系统直接持有音箱的语音通道（`speech.usock`），完成唤醒后的收音、判停、播报与恢复，并且不打断正在播放的音乐。

## ADDED Requirements

### Requirement: 持有语音通道

语音宿主启动后 SHALL 在 `/tmp/mico_aivs_lab/usock/speech.usock` 上注册并接管 mipns 的语音流；原生的 `mico_aivs_lab` MUST NOT 再处理语音。

#### Scenario: 启动后接管成功

- **WHEN** 语音宿主模式启动
- **THEN** 该 socket 由本系统持有，mipns 向它发送注册、唤醒与 ASR 音频

#### Scenario: 原生助手不再参与

- **WHEN** 用户喊「小爱同学」并提问
- **THEN** 不会出现小爱原生的回答或动作

### Requirement: 唤醒后收音与判停

收到原生唤醒事件后，系统 SHALL 请求一次 ASR 收音，使用收到的 PCM 做语音识别，并在检测到句末静音后结束本次收音。

#### Scenario: 唤醒后说一句话

- **WHEN** 用户喊「小爱同学，今天天气怎么样」
- **THEN** 系统拿到这句话的音频并完成识别，文本进入主助手

#### Scenario: 说完自动结束

- **WHEN** 用户说完后保持安静
- **THEN** 系统在静音阈值后结束收音，不依赖原生 VAD 的会话关闭

### Requirement: 播报不打断音乐

回答播报 SHALL 使用与设备播放器相互独立的通道；播报期间音乐仅降低音量，MUST NOT 停止或重启播放器，播报结束后 SHALL 恢复原音量。

#### Scenario: 播报后音乐接着放

- **WHEN** 音乐播放中完成一轮问答
- **THEN** 音乐从原来的播放位置继续，不从头开始

### Requirement: 可回退

系统 SHALL 提供停止语音宿主并恢复原生语音服务的入口；停止后原生小爱与现有冻结方案 MUST 可重新工作。

#### Scenario: 停止接管

- **WHEN** 执行停止命令
- **THEN** `pns` 与 `mico_aivs_lab` 恢复原生配置，语音重新由小爱处理

### Requirement: 看护与自愈

语音宿主或其 socket 被外部替换时，系统 SHALL 在有限时间内发现并恢复接管；连续失败 MUST 停止重试并回落到原生链路。

#### Scenario: socket 被重建

- **WHEN** `mico_aivs_lab` 重建了 `speech.usock`
- **THEN** 看护逻辑发现 inode 变化并恢复接管，或按上限停止重试

### Requirement: 模式开关

系统 SHALL 提供配置项在「冻结播放器」与「语音宿主」两种接管方式之间切换，默认保持现有冻结方案。

#### Scenario: 默认行为不变

- **WHEN** 未开启语音宿主模式
- **THEN** 现有冻结方案与本地唤醒链路的行为与变更前一致
