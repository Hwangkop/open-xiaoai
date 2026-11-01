# Design

## Context

参见 `proposal.md - Why`。当前实现的相关事实（读码确认）：

- `apps/chat/speaker.go` 918 行，13 个包级状态变量：`speakingFlag`、`speakingCooldownUntil`、`musicPlayingFlag`、`musicStartedAt`、`pendingMusicURL`、`musicTrackingRunning`、`musicDuck{Enabled,Target,Original,Applied,Busy,Muted,MutedPrev}`、`thinkingLEDOn/At`、`ledDeadline`、`thinkingSilenceOn`。
- `engine.onUtterance` 是一个 6 段 if 树（长度门槛 → 音乐白名单 → 歌词 → 过短 → 词面 → 路由）；`handleMusicVoice` 与 `routeMusicText` 又各自带分支。
- `router_agent.go` 的 `routingActive` 是包级 `atomic.Bool`，决定 `play_music` 立刻起播还是排队等 AI 回复。
- 测试里要手写 `resetMusicDuck()` 清理 6 个全局变量；`Speaker` 无法多实例。
- 外部契约：管理台 API、MCP 工具名、codex 线程语义都要保持不变。

## Goals / Non-Goals

**Goals:**

- 语音状态只有一个所有者，状态转移是显式、原子、可测的。
- "这句话要不要处理、走哪条路"变成一个纯函数判定，可在无模型、无设备的情况下测试。
- 保留现有对外行为（用户听到的效果不变）。

**Non-Goals:**

- 不重写 ASR / TTS / codex 客户端。
- 不拆服务、不改管理台 API 形态。
- 不引入状态机或 DI 框架。

## Decisions

### D1：状态收进 `Speaker` 实例，用一把锁保护组合状态

`Speaker` 新增内部结构：`playState{phase, music, duck, mute, led, pendingURL}`，所有读写经 `Speaker.mu`。

- 替代方案：继续用独立 `atomic`（现状）——放弃，因为跨字段不变量无法保证（今天的 5 个 bug 全出自此）。
- 替代方案：引入 `sync.Map` 或事件总线——放弃，过度设计。

对外方法改为语义化：`BeginSpeaking()/EndSpeaking()`、`EnterMusic(url)`、`AdjustMusicVolume(delta)`、`SetMuted(bool)`、`RestoreAfterMusic()`、`ShowActivityLED()/HideActivityLED()`。旧方法名保留薄封装，减少调用点改动。

### D2：阶段用枚举，不用多个布尔

```go
type VoicePhase int
const (
    PhaseIdle VoicePhase = iota
    PhaseListening      // 正在听（可选）
    PhaseRecognizing    // 已切句，正在识别
    PhaseThinking       // 交给模型
    PhaseSpeaking       // 正在播报
    PhaseMusic          // 混合态：在放音乐
)
```

- `Speaking()`、`MusicPlaying()` 等查询改为 `Phase == X` 或带冷却的组合判断，语义集中。
- 音乐态与说话可以同时存在（放歌时 TTS 播报），因此 `music` 作为**并列的子状态**而不是互斥阶段；阶段只表达"当前主导动作"。

### D3：`decide()` 纯函数

```go
type Decision struct {
    Path   Path      // Whitelist / Lyric / Route / Dialog / Drop
    Action string    // next/stop/volume_up/...（Path=Whitelist 时）
    Reason string    // 日志与测试用
}
func (e *Engine) decide(text string, st Snapshot) Decision
```

- `Snapshot` 只带判定需要的只读状态（是否放歌、是否在对话窗口、是否命中唤醒词、配置）。
- `onUtterance` 变成：取快照 → `decide` → `switch d.Path` 执行副作用。副作用与判定彻底分离。
- 表驱动测试直接覆盖 spec 里的每个 scenario。

### D4：路由执行上下文显式传递

`routingActive` 全局开关改为 `routeCtx` 参数一路传到 `searchAndPlay`（经 MCP 无法传参的路径，用 `Speaker` 的 `musicExpectImmediatePlay` 子状态表达，随路由生命周期置位/清除）。

- 替代方案：给 MCP 加会话头——codex 侧无法按调用传，放弃。
- 折中：路由开始时设置"立即起播"子状态（由 `Speaker` 拥有），路由结束时清除；这与 D1 的单一所有者一致。

### D5：迁移顺序（先加新、再迁移、后删旧）

1. 新增 `playState` + 锁 + 语义方法，内部实现先复用现有全局变量（行为不变）。
2. 逐一把调用点切到语义方法。
3. 删除全局变量与临时 reset 逻辑。
4. 引入 `decide()`，`onUtterance` 改为分发；旧分支逻辑逐条搬进 `decide()` 并用测试固定。
5. 补齐 spec 场景对应的测试。

## Risks / Trade-offs

- [热点文件大改导致回归] → 分 5 步迁移，每步跑全量 `-race` 测试 + 部署验证一次；任一步失败可单独回滚。
- [锁粒度变大导致阻塞] → `Speaker` 的锁只保护内存状态，不包住设备 shell 调用；设备操作在锁外执行，用"先取快照 → 执行 → 按结果写回"的方式。
- [阶段枚举替代布尔后语义遗漏] → 为每个旧布尔写一条"旧查询 = 新查询"的等价测试，迁移期间同时跑。
- [decide() 与现有行为不一致] → 每个 scenario 先写测试、再改实现；行为差异必须显式记录在 tasks 里。
- [路由立即起播的标记放错位置] → 用集成测试覆盖：路由期间调用 `play_music` 必须触发 `PlayPendingMusic`。

## Migration Plan

- 部署：与现在相同（交叉编译 → taskkill → scp → schtasks run）。每步迁移后部署一次观察日志。
- 回滚：每个迁移步骤是一个独立提交，出现问题按提交回退；`decide()` 切换前保留旧分支代码一个版本，用配置开关（临时）切换。
- 数据：无 schema 变更，无迁移脚本。
