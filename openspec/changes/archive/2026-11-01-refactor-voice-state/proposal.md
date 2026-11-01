# Proposal

## Why

`apps/chat` 里的语音状态（谁在说话、在放歌、在路由、灯亮不亮）目前由 **13 个包级原子变量** 分散持有，判断逻辑散在 `engine.onUtterance`、`Speaker.handleMusicVoice`、`routeMusicText` 三处。同一天内因此产生了 5 个真实缺陷：静音后音量卡在 0、还原记录被误删、并发播放记错原值、路由点歌只入队不出声、指示灯被底噪点亮后常亮。

每新增一个能力（音乐态路由、指示灯、ducking）都要再插一个布尔和一处 if，状态组合是乘性的，继续堆下去修 bug 的成本会超过加功能的收益。

## What Changes

- **BREAKING（内部 API）**：`Speaker` 的包级状态（`speakingFlag`/`musicPlayingFlag`/`musicStartedAt`/`musicDuck*`/`method` 组、`pendingMusicURL`、`thinkingLEDOn` 等）收拢为 `Speaker` 实例内的单一 `playState`，由一把锁保护，对外只暴露语义方法。
- 引入语音生命周期状态机 `VoicePhase`（idle / listening / recognizing / thinking / speaking / music），所有"现在能不能听/能不能打断"的判断集中到一个 `CanAccept()` 决策点。
- 把 `engine.onUtterance` 的分支树抽成纯函数 `decide(text, state) Decision`，可用表驱动测试覆盖。
- `routingActive` 这个隐式全局开关改为路由阶段显式传入的上下文标记。
- 为以下行为补齐回归测试：duck/静音/还原的所有组合、路由点歌立即起播、指示灯只在对话/路由时点亮。

## Capabilities

### New Capabilities

- `voice-session-state`: 语音会话状态的唯一所有者与合法转移（说话/放歌/静音/指示灯），对外只暴露语义操作，禁止外部直接改字段。
- `voice-routing`: 一句话进入系统后走到哪条路径的判定（唤醒 → 白名单 → 歌词 → 语义路由 → 丢弃），判定结果是纯函数且可测试。

### Modified Capabilities

（无：项目此前没有 OpenSpec 规格。）

## Impact

- 代码：`apps/chat/speaker.go`（拆分）、`engine.go`（决策函数化）、`router_agent.go`/`router_codex.go`（路由标记显式化）、`mic.go`（可选）、相关测试。
- 行为：对外语义不变（用户听到/看到的效果与今天一致），仅内部结构变化；`Speaker` 的方法签名会调整，调用点需同步。
- 风险：`speaker.go` 918 行是热点文件，改动面大；用「先加新结构、逐步迁移调用点、最后删旧字段」的方式降低风险。
- 不影响：ASR/豆包/codex 客户端、管理台 API、MCP 工具的外部契约。
