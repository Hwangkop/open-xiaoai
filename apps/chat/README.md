# Open-XiaoAI x Chat (Go)

**小爱音箱的 AI Server**：单 Go 二进制，自带管理台、MCP 工具与音乐态语音控制。

对话后端可切换：**codex**（常驻 app-server，可调 MCP 工具）、**豆包网页直连**（免费、带联网）、或任意 **OpenAI 兼容 API**（GPT / Claude / DeepSeek / 通义千问）。回复走文本流式 + TTS，可随时打断。

## 功能

- **关键词触发**：仅回答以「请」「你」等关键词开头的消息
- **关键词/唤醒词打断**：仅当配置的关键词或唤醒词匹配时才打断 AI 回复
- **文本流式 + TTS**：Chat Completions 流式输出，逐句 TTS 播放
- **对话历史**：可配置的上下文长度
- **自定义回复**：通过配置文件设置固定回复（文字/音频链接）
- **中断机制**：新消息到来时自动取消正在进行的 AI 回复
- **MCP 工具**：内置 22 个工具（音乐 / 播报 / 定时任务 / 家电 / 搜索），供对话后端直接调用
- **音乐态语音控制**：放歌时说「下一首」「停下」「大点声」不用喊唤醒词
- **语音态路由**：带唤醒词的话先在工具路由 / 分类器 / codex 之间分流，判成动作就直接执行
- **Web 管理页**：配置、唤醒词、路由漏斗、智能体上下文、TTS 试播

## 对话后端

配置项 `chat.provider`：

| 取值 | 说明 |
|---|---|
| `codex`（推荐） | 常驻 `codex app-server`（JSON-RPC），**能调用 MCP 工具**：放音乐、设提醒、控家电、联网搜索。会话落盘，重启可恢复；一个进程里可开多条 thread（主对话 / 音乐路由 / 定时任务），上下文互不干扰。 |
| `doubao_web` | 豆包网页版直连，免费、自带联网搜索，但没有工具调用。 |
| `llm` | 任意 OpenAI 兼容接口（GPT / Claude / DeepSeek / 通义千问），可控性最强。 |

codex 的相关配置在 `codex.bin` / `codex.workspace` / `codex.codex_home`：`CODEX_HOME` 里放 `config.toml`（模型供应商 + MCP server 指向本服务的 `/mcp`）与 `models.json`。

## MCP 工具

本服务在 `/mcp` 暴露 22 个工具（只监听 127.0.0.1，给同机的 codex 用）：

- **音乐**：`play_music` `search_music` `play_playlist` `play_daily` `list_playlists` `next_music` `stop_music` `set_volume`
- **播报**：`speak` `set_tts_volume`
- **定时任务**：`create_task` `list_tasks` `update_task` `set_task_enabled` `run_task` `stop_task` `delete_task`
- **家电**：`list_devices` `control_device`（走 Home Assistant）
- **信息**：`web_search` `get_time` `end_conversation`

对话链路里写死的分支越来越少：音乐态的"工具路由"是**一次 function calling**，模型直接从这 22 个工具里选，选完由同一套 `runMCPTool` 执行。

## 音乐态

放音乐时麦克风持续收到歌词，所以判定分四层（实现见 `voice_decide.go`，纯函数、可测试）：

1. **本地白名单**（免唤醒）：`下一首 / 停下 / 安静 / 大点声 / 小点声 / 静音`——要求"基本就是这句指令"
2. **带唤醒词** → 交给路由
3. **无唤醒词但含动作词**（换 / 放 / 听 / 播 / 收藏）→ 交给路由
4. 其余一律丢弃，不调模型

路由本身也是三层的：**进程内工具路由**（约 1.8s，能调全部 MCP 工具）→ **轻量分类器**（约 0.6s，固定 intent）→ **codex 路由线程**（兜底）。判成动作就直接执行，判成对话才交给主助手。

另外两个配套行为：

- **降音量**：起播前把音量压到 `music_duck.volume`（默认 35），停播还原；用户手动调过就不覆盖。
- **指示灯**：只在"这句话要交给路由或主助手"时点亮，噪声不亮，最后一次活动后自动熄灭。

## 快速开始

> [!NOTE]
> 需先在小爱音箱上运行 [apps/client](../client/README.md) 补丁程序。

### 1. 获取二进制

**方式 A：从 Releases 下载预编译二进制（推荐）**

```shell
# Linux x86_64
curl -L -o /tmp/chat.tar.gz \
  https://github.com/cxjava/open-xiaoai/releases/latest/download/chat_Linux_x86_64.tar.gz
tar -xzf /tmp/chat.tar.gz -C /opt/open-xiaoai-chat

# macOS arm64 / Windows amd64 等其他平台请到 Releases 页选择对应包
```

Releases 同时提供 `chat_Linux_arm64`、`chat_Darwin_x86_64`、`chat_Darwin_arm64`、`chat_Windows_x86_64.zip` 等。

**方式 B：从源码构建**

```shell
cd apps/chat
bash build.sh   # 产物在 dist/chat
```

**方式 C：在任意平台交叉编译（含 Windows）**

```shell
# Windows x86_64
just build-chat windows amd64     # 产物：dist/chat_windows_amd64.exe

# 构建 Windows 版
just build-windows

# 其他平台同理，例如 Linux arm64
just build-chat linux arm64
```

Windows 上不需要额外安装任何运行时：Go 产物是静态链接的单文件，双击或在 PowerShell 里直接运行即可：

```powershell
.\chat.exe -config config.yaml
```

> [!NOTE]
> 服务监听、配置文件路径都按 Windows 习惯工作（配置里用反斜杠或正斜杠都可以）。TTS 合成音频写入配置文件同目录下的 `tts/`，便于跨服务重启复用。

### 2. 编辑配置

```shell
cd apps/chat
vim config.yaml
```

修改 `llm` 配置：

```yaml
llm:
  base_url: "https://api.openai.com/v1"   # 或 OpenRouter / DeepSeek 等
  api_key: "sk-xxxxxxxxxxxxxxxx"
  model: "gpt-4.1-mini"

# Claude 示例（通过 OpenRouter）：
# llm:
#   base_url: "https://openrouter.ai/api/v1"
#   api_key: "sk-or-xxx"   # OpenRouter API Key
#   model: "anthropic/claude-3.5-sonnet"
```

### 3. 运行

```shell
# 默认读取当前目录的 config.yaml
./chat

# 指定配置文件
./chat -config /path/to/config.yaml

# 跑两套 Server（如家里 + 异地）：把其中一个改成 4400 端口（config.yaml 中 server.port: 4400）
# 音箱上 client 使用切换模式：./client -switch ws://IP:4399 ws://IP:4400
```

### 4. 连接音箱

确保小爱音箱的 client 已连接到本机 `ws://你的IP:4399`。

### 5. 打开管理前端

`apps/chat` **只提供 API**，管理台是独立的前端项目，源码在 [`web/`](web/)，技术栈为 React 19 + Vite + Tailwind CSS v4（组件与主题变量取自 ZCode 的 UI 包）。

```shell
cd apps/chat/web
pnpm install
pnpm build          # 产物在 apps/chat/web/dist
```

或者用项目自带的 just 任务：

```shell
just web-install    # 安装依赖
just web-build      # 构建到 apps/chat/web/dist
just web            # 本地预览（:5500，需先 web-build）
just web-dev        # 开发模式，热更新（:5173）
```

把 `dist/` 交给任意静态服务器即可（nginx、Caddy、`python -m http.server` 都行）。

打开页面后，在「连接设置」里填入 chat 的地址（例如 `http://你的IP:4399`）；如果后端配置了 `auth.users`，同时填用户名和密码，点「保存并测试」。

因为前后端是跨域访问，后端会自动返回 CORS 头。默认允许任意来源；如需收紧，在 `config.yaml` 里配置：

```yaml
cors:
  allowed_origins:
    - "http://192.168.1.10:5500"
```

管理页支持：

- 查看和编辑当前 `-config` 指定的 YAML 配置文件
- 保存后覆盖原 YAML 文件
- 热加载 `prompt`、关键词、自定义回复等后续请求会读取的配置
- 热重建 `music` 模块，包括启用/关闭音乐、目录、LX、音乐关键词等
- 在 TTS 文本框输入一段话，发送到已连接的小爱音箱播放
- **豆包扫码登录**：登录过期时点一下「扫码登录」，弹出二维码，用豆包 App 扫一下就完成续期
- **豆包会话记录**：列出最近的豆包对话，点击查看消息记录
- **唤醒与识别**：管理自定义唤醒词、在线测试一句话能不能唤醒、切换识别引擎、查看最近识别结果
- **音乐态路由**：每句话走到哪一层（白名单 / 歌词 / 路由 / 丢弃）、路由器判成什么、置信度与耗时
- **智能体**：主对话 / 音乐路由 / 定时任务三条 codex 会话的上下文管理（压缩、新开会话）

`server.host` / `server.port`、`llm.*`、`proxy` 这类会影响监听地址或 LLM client 构造的配置，保存后会写入文件，但需要重启 `chat` 后完全生效。

## 豆包扫码登录

使用 `doubao2api` 作为聊天后端时，豆包网页 session 大约 30 天过期。过期后不需要手动翻 Cookie，直接在管理页续期：

1. `config.yaml` 里配置 doubao2api 地址：

   ```yaml
   doubao2api:
     base_url: "http://127.0.0.1:9090"
     api_key: ""   # 与 doubao2api 的 DOUBAO_API_KEY 一致，未配置则留空
   ```

2. 打开 `http://你的IP:4399/admin`，页面顶部「豆包登录」会显示当前状态。
3. 点「扫码登录 / 刷新登录」，用豆包 App 扫码。
4. 页面每 2 秒轮询一次，登录成功后提示「登录成功，session 已更新」。

实现上这是对 doubao2api 自带接口的转发（`POST/GET /v1/session/qr-login`），cookie 由 doubao2api 自己保存并注入它的 Playwright 浏览器，所以不需要也不应该在本项目里另存一份。把 `doubao2api.base_url` 留空即可隐藏这个面板（例如切换成官方 ARK 时）。

## 豆包会话记录

配置了 `doubao2api.base_url` 后，管理页还有「豆包会话记录」卡片：点「加载 / 刷新会话」列出最近的豆包对话（标题、最后一条消息预览、时间），点任意一条查看消息记录，用户消息和豆包回复分开显示。

豆包的会话接口（`/im/chain/recent_conv`、`/im/chain/single`、`/im/conversation/batch_get`）需要网页 Cookie 和网关签名，Go 侧发不出去。这里的做法是通过 doubao2api 的 `/auth/eval`，在它已经登录的浏览器页面里执行一段脚本，由页面自己带着 Cookie 和签名去请求，再把结果整理成 JSON 返回。因此：

- 需要 doubao2api 的浏览器处于就绪状态（登录正常）
- 只读取文本消息，图片/视频/卡片类内容会跳过
- 会话列表取的是"最近会话"，不是全量历史翻页
- 这是只读能力，不会修改任何豆包数据

## 配置说明

与语音/音乐相关的几块配置：

```yaml
chat:
  provider: "codex"     # codex | doubao_web | llm

codex:
  bin: "codex"
  workspace: "/path/to/empty-dir"     # 别让模型看到项目文件
  codex_home: "~/.codex-deepseek"     # 里面放 config.toml + models.json
  timeout_seconds: 60

# 放音乐时自动降音量（让麦克风听得清人声）
music_duck:
  enabled: true
  volume: 35

# 音乐态语义路由
music_router:
  enabled: true
  engine: "llm"          # llm：分类器/工具路由优先；codex：全部走 codex 线程
  timeout_ms: 1500
  min_confidence: 0.6
  prompt: ""             # 留空用内置提示词
```


| 配置项 | 说明 |
|--------|------|
| `server.host` / `server.port` | 服务端监听地址和端口 |
| `llm.base_url` | API 地址，支持 OpenAI / Claude(OpenRouter) / DeepSeek / 通义千问等 |
| `llm.api_key` | API 密钥 |
| `llm.model` | 模型名称 |
| `prompt.system` | 系统提示词（支持 `{{now}}` / `{{date}}` 占位符） |
| `prompt.inject_time` | 请求时注入当前时间，默认 true，不写入历史 |
| `prompt.timezone` | 时间所用时区，默认 Asia/Shanghai |
| `context.history_max_length` | 对话历史条数（0=关闭） |
| `interrupt.keywords` | 打断触发的关键词（空则用 call_ai_keywords） |
| `interrupt.match_mode` | 匹配模式：exact / prefix / contains |
| `interrupt.kws_interrupt` | 唤醒词是否触发打断 |
| `call_ai_keywords` | 触发 AI 的关键词列表 |
| `auth.users` | WebSocket 与 Web 管理页共用的 Basic Auth 用户列表（为空则跳过认证） |
| `greeting` | 连接成功后播放的提示语 |
| `error_message` | 出错时的提示语 |
| `custom_replies` | 固定回复规则（match + text/url） |
| `tts.provider` | `xiaoai`（默认，音箱自带 TTS）或 `doubao`（豆包音色） |
| `tts.speaker` | 豆包音色 ID，默认 `zh_female_taozi_conversation_v4_wvae_bigtts` |
| `tts.format` | 音频格式：`wav`（默认）/ `mp3` / `ogg_opus` / `pcm` |
| `tts.player` / `tts.wav_device` | 默认 `auto` / `notify`；WAV 用 `aplay -D notify`，其他格式使用 miplayer |
| `tts.volume` | 0-100，默认 60；映射 notifyvol，音乐音量独立，其他 notify 提示音可能受影响 |
| `native_wake.takeover_enabled` | 原生 ASR 接管开关，默认关闭 |
| `native_wake.isolation_mode` | 默认 `freeze`；`instruction` 需设备匹配钩子和在线租约，按 dialog 过滤原生动作 |
| `tts.bit_rate` / `tts.sample_rate` | 码率与采样率，默认 32000 / 24000 |
| `doubao2api.base_url` | 本机 doubao2api 地址，用于管理页扫码登录；留空则隐藏该面板 |
| `doubao2api.api_key` | 与 doubao2api 的 `DOUBAO_API_KEY` 一致，未配置则留空 |

认证配置示例：

```yaml
auth:
  users:
    - username: "admin"
      password: "请换成强密码"
```

> [!IMPORTANT]
> 管理页会返回完整 YAML 内容，包括 `llm.api_key`。如果服务暴露在局域网之外，请务必配置 `auth.users`，并优先通过内网、VPN 或带 TLS 的反向代理访问。

## 豆包语音合成

默认情况下，AI 回复由音箱自带的 `/usr/sbin/tts_play.sh` 朗读。把 `tts.provider` 设为 `doubao` 后，回复改用豆包网页版「自动播报」同一套语音合成，音色更自然：

```yaml
tts:
  provider: "doubao"
  speaker: "zh_female_taozi_conversation_v4_wvae_bigtts"
  format: "wav"
  player: "auto"
  wav_device: "notify"
  volume: 60        # 0-100，映射到 notifyvol，不调 mysoftvol
  bit_rate: 32000
  sample_rate: 24000
  speech_rate: 1.0   # 语速，1.0 正常，>1 更快
  pitch: 0.0         # 音调，0 正常
```

常用音色（与火山引擎豆包语音一致）：

| 音色 ID | 说明 |
|---------|------|
| `zh_female_taozi_conversation_v4_wvae_bigtts` | 桃子 · 女声对话（默认） |
| `zh_female_tianmei_conversation_v4_wvae_bigtts` | 甜美 · 女声 |
| `zh_female_shuangkuai_emo_v3_wvae_bigtts` | 爽快 · 女声 |
| `zh_female_qingche_moon_bigtts` | 清澈 · 女声 |
| `zh_male_yangguang_conversation_v4_wvae_bigtts` | 阳光 · 男声 |
| `zh_male_chenwen_moon_bigtts` | 沉稳 · 男声 |
| `zh_male_rap_mars_bigtts` | 说唱 · 男声 |
| `en_female_sarah_conversation_bigtts` | 英文女声 |
| `en_male_adam_conversation_bigtts` | 英文男声 |

工作方式：`apps/chat` 收到 AI 逐句回复后，先在服务端合成音频（默认 WAV，缓存到配置文件同目录下的 `tts/`），再通过本服务新增的 `/tts/<文件名>` 路由把音频地址交给音箱播放，因此**音箱必须能访问到本服务的端口**（与本地音乐使用同一套连接感知 host）。

实现细节：该接入复用豆包网页端 `voicegenie` WebSocket，协议为手写 protobuf 帧（`StartTask → StartSession → BidirectionalTTS → EndTTS`），音频分片直接拼接。目前该端点不需要登录 Cookie，属于逆向实现，接口变动时可能需要重新适配。

原生唤醒音乐共存的固件范围、重启后验收和开机加载步骤见 [根 README](../../README.md#接管小爱同学并保留背景音乐) 与 [原生指令隔离说明](../../tools/native-guard/README.md)。通过 MCP 调整的 TTS 音量保存在配置同目录的 `tts-volume.json`，启动时恢复；保存配置后以 `tts.volume` 为准。管理台 `/ui/#audiotest` 可对比播放通道和音量，属于调试入口。

## 定时任务播报

豆包的「定时任务」是独立功能页（`/chat/cron-jobs`），聊天接口里没有创建任务的入口。本项目的做法是**只读**：

1. 你在豆包网页/App 里创建任务（工作任务 / 项目 / 技能 / 插件都行，界面会做校验）
2. `apps/chat` 每 60 秒轮询 `/alice/job_cron/list`，用 `last_run_time` 判断有没有新执行
3. 有变化就读取该任务的 `conversation_id` 会话，取最新一条 assistant 消息
4. 清洗成适合朗读的纯文本 → 豆包 TTS → 音箱播报

```yaml
schedules:
  enabled: true
  poll_interval: "60s"        # 最小 10s
  quiet_hours: "23:00-07:00"  # 留空表示全天播报
```

行为约定：

- **首次启动只记录基线**，不补播历史结果
- 按 `message_id` 去重，同一个结果不会播两遍
- 播报**串行排队**，不会和正在播放的音乐/对话叠在一起
- 文本会去掉 Markdown、emoji、链接和代码块，只保留可朗读内容
- 静默时段内照常记录状态，只是不出声
- 状态文件在配置文件同目录的 `schedule-state.json`

管理台「定时任务」页可以查看任务列表、上次结果，并提供「立即轮询一次」和「播报」两个调试按钮。

> 之所以不通过接口创建任务：`schedule` 结构随版本变化，手工拼容易建错，交给界面最稳。实测把 `repeat_type/time` 拼成"每小时第 51 分"后任务并未按时触发。

## 本地音乐

已集成 [pkg/music](../../pkg/music/README.md)。在 `config.yaml` 中启用：

```yaml
music:
  enabled: true
  dirs:
    - /path/to/music
```

支持「播放许嵩」「随便听听」「停止播放」等语音指令，也支持默认故事上下文（如先说「默认故事是三国演义第一季」，再说「播放第4集」）。连接感知 base_url 已启用，音乐 URL 会根据客户端连接方式（LAN 或 Tailscale）自动选择 host。详见 [connection-aware-base-url-design](../../docs/connection-aware-base-url-design.md)。

通过 `/admin` 修改 `music` 配置并保存后，`apps/chat` 会停止旧的音乐模块并按新配置重新启动；如果只修改监听端口、LLM 或代理配置，仍需重启 `chat`。

## 自定义回复示例

```yaml
custom_replies:
  - match: "测试播放文字"
    text: "你好，很高兴认识你！"
  - match: "测试播放音乐"
    url: "https://example.com/hello.mp3"
```

## 注意事项

- 默认监听 `0.0.0.0:4399`，可在 config 中修改
- 默认 `auth.users: []` 时，WebSocket 和 `/admin` 管理页都不需要认证；生产或跨网段访问前请先配置认证
- 默认不开启录音/播放，仅处理 instruction 事件（语音识别结果）
- 如需接收原始音频流，需在 client 端启用 start_recording / start_play
