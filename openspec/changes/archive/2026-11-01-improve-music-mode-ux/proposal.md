# Proposal

## Why

音乐态路由一轮要 4 秒（codex 线程），而"常用指令也要能调 MCP"这件事在固定 intent 分类器里做不到——每加一个能力都得写 Go 分发分支。同时，点歌后没有任何反馈（放错了也听不出来），播报又会被自己的麦克风听回去、并打断音乐态。

## What Changes

- 路由主路径改为**进程内工具路由**：复用 mcpToolList() 的 21 个 schema 做 function calling，直接调 runMCPTool；分类器退为第二层，codex 线程退为兜底。
- 点歌成功后播报一句确认（≤40 字）；其它动作不播报。
- 播报期间不计入音乐停止判定；播报结束自动 resume / 重新拉起当前曲目。
- 播报结束后的 ASR 抑制窗口 700ms → 1500ms，避免把自己的确认语当成新指令。

## Capabilities

### New Capabilities

（无）

### Modified Capabilities

- `voice-routing`: 路由契约改成三层；新增"点歌口头确认"要求。
- `voice-session-state`: 新增"播报不打断音乐态""播报回声不回流"两条要求。

## Impact

- 代码：`apps/chat/router_inproc.go`（新增）、`router_agent.go`、`router_codex.go`、`speaker.go`、`admin.go`、`engine.go`。
- 依赖：无新增；复用 go-openai 的 tools 参数。
- 行为：常用指令 4.3s → 1.8s；点歌会多一句确认语。
