# Tasks

## 1. 状态收拢（D1/D2）

- [x] 1.1 在 `speaker.go` 引入 `playState` 结构与 `Speaker.mu`，把 13 个包级状态变量改为 `playState` 字段；原包级变量保留为"薄转发"实现，保证行为不变。验证：`go build ./... && go test -race -short ./...` 全绿，部署后日志与改动前一致。
- [x] 1.2 为所有状态读写加锁：设备操作（shell/TTS 播放）必须在锁外执行，锁内只做"取快照/写回"。验证：`go test -race` 通过；代码审查确认 `runShell`/`PlayURL` 调用不在持锁区间。
- [x] 1.3 新增 `VoicePhase` 枚举与 `Snapshot()` 查询，把 `Speaking()`、`MusicPlaying()` 等改为基于阶段实现的等价查询。验证：新增等价测试，逐条断言"旧布尔为真 ⟺ 新查询为真"。

## 2. 语义方法迁移（D1）

- [x] 2.1 【实际做法】状态已全部封装进 `playState`，对外访问一律经由 `Speaker` 的既有语义方法（`PlayTTS`/`DuckMusicVolume`/`SetMusicMuted`/`RestoreMusicVolume`/`ShowThinkingLED`/`QueueMusic` 等），并新增 `BeginImmediatePlay`/`Phase`/`stateSnapshot`。**未做大面积重命名**——原方法名已经在表达语义，重命名只会扩大改动面。验证：现有 25 个语音相关测试全部通过。
- [x] 2.2 迁移所有调用点到语义方法，删除包级变量引用。验证：`grep -rn "speakingFlag\|musicPlayingFlag\|musicDuck(Original|Applied|Enabled|Target|Busy|Mu)\|musicMutedPrev\|pendingMusicURL\|musicStartedAt\|thinkingLEDOn\|thinkingSilenceOn\|routingActive" apps/chat/*.go` 返回空；`speaker.go` 仅剩 2 个配置型包级变量（`stopTTSTimeoutMs`、`musicDuckFile`）。
- [x] 2.3 删除测试里的 `resetMusicDuck()` 全局清理，改为每个测试独立构造 `Speaker`。验证：`speaker_duck_test.go` 不再包含全局变量重置代码，`-race` 通过。

## 3. 决策函数化（D3）

- [x] 3.1 定义 `Decision`/`Path`/`Snapshot`，实现 `decide(text, snapshot)`，先把现有 if 树逐条搬进去（行为不变）。验证：新增表驱动测试覆盖现有全部白名单词与唤醒/丢弃分支。
- [x] 3.2 `onUtterance` 改为"取快照 → decide → 分发副作用"，副作用保持原实现。验证：`go test -race -short ./...` 通过；部署后喂同样 6 句话（唤醒/裸指令/歌词/噪声/长句/路由）日志判定与改动前一致。
- [x] 3.3 补齐 spec 场景测试：音乐态裸指令、带上下文不命中、噪声零成本丢弃、唤醒词优先白名单、非音乐态丢弃。验证：测试名与 spec scenario 一一对应，全部通过。

## 4. 路由执行上下文（D4）

- [x] 4.1 把 `routingActive` 全局开关改为 `Speaker` 拥有的"路由期间立即起播"子状态，随路由开始/结束置位与清除。验证：单测覆盖"路由中 play_music 立即起播""非路由中排队等回复"两种情况。
- [x] 4.2 集成验证：真机放音乐 → 说"豆包豆包我想听周杰伦的歌"，确认 `music-state` 变为目标歌曲且 `music=true`。验证：日志出现 `🎵 播放` 且随后 `🔉 音乐音量`、管理台显示播放中。

## 5. 收尾

- [x] 5.1 删除迁移期遗留的薄转发与兼容代码，更新 `apps/chat` 相关注释。验证：`go vet ./...` 干净，`grep` 无遗留全局状态。
- [x] 5.2 全量回归：`go test -race -short ./...` + 真机走一遍（放歌/切歌/停止/静音/对话/路由/指示灯）。验证：全部符合 spec 中的 scenario。

## Workflow follow-up

- 归档：`openspec archive refactor-voice-state`，把两条 capability 同步进 `openspec/specs/`。
