# AGENTS.md

面向在本仓库里工作的 AI agent / 协作者。**部署、刷机、运维的完整步骤见 [docs/deploy.md](./docs/deploy.md)**，动手前先读它。

## 仓库结构

- `apps/client`：跑在音箱上的 Go 客户端（linux/arm/v7）
- `apps/chat`：AI Server（对话、ASR、TTS、MCP 工具、管理台），单二进制
- `apps/chat/web`：管理台前端（React + Vite + Tailwind）
- `pkg/music`、`pkg/netease`、`pkg/homeassistant`：可复用库
- `tools/client-patch`：固件制作；`tools/flash`：macOS 刷机工具；`tools/asr-worker`：ASR worker

每个 Go 模块有独立 `go.mod`，在各自目录内构建。

## 常用命令

```bash
# 服务端
cd apps/chat && go build ./... && go test ./...

# 音箱端（交叉编译）
cd apps/client && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build .

# Windows 服务端
cd apps/chat && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o ../../dist/chat_windows_amd64.exe .

# 管理台
cd apps/chat/web && pnpm install && pnpm build
```

## 约定

- 注释、日志、错误信息用中文；日志前缀带场景 emoji（`🎤` `🗣️` `🎵` `👂` 等），保持现有风格。
- 新增配置项写进 `apps/chat/config.yaml` 并加注释；管理台表单字段在 `apps/chat/web/src/views/ConfigView.tsx` 的 `SECTIONS` 里登记。
- MCP 工具在 `apps/chat/mcp.go`：先加工具定义，再加 `runMCPTool` 分支。
- 前端路由用 hash（`#wake`），沿用现有管理台布局与组件。

## 红线

1. **不要提交** `.env`、`*-session.json`、config 里的 token/密码；文档里也不写真实值。
2. **不要跨平台整份覆盖 `config.yaml`**（Windows/Mac 路径不同，会打断 ASR/服务）。
3. 更新远端二进制顺序：`taskkill → 覆盖 → 启动`（运行中的 exe 被锁，先覆盖会静默失败）。
4. 改 `prompt.system` 后要重启服务（codex 的 `baseInstructions` 建 thread 时固定）。
5. 本项目以单二进制部署，**不引入 Docker 运行时依赖**。
6. 家电控制统一走 Home Assistant，不引入小米云第三方签名直连。

## 验证

- 本地先 `go build ./... && go test ./...`，前端 `pnpm build`。
- 部署后在管理台 `/ui/` 自检：连接状态、配置页、唤醒与识别、语音测试。
- 日志：服务端 `chat.log`；ASR 识别耗时/引擎在 `⏱️ [识别]` 行。
