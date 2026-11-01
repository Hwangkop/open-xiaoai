# Open-XiaoAI x Chat (Go)

**文本流式 + TTS**：小爱音箱接入 OpenAI 兼容 API 的 Go Server，单二进制部署。

支持 **GPT、Claude、DeepSeek、通义千问** 等（文本流式 + TTS 播放）。与 apps/gemini 的实时音频不同，apps/chat 为文本流式 + TTS 模式，可完美打断小爱回复，响应延迟低。Claude 可通过 [OpenRouter](https://openrouter.ai) 使用。

## 功能

- **关键词触发**：仅回答以「请」「你」等关键词开头的消息
- **关键词/唤醒词打断**：仅当配置的关键词或唤醒词匹配时才打断 AI 回复
- **文本流式 + TTS**：Chat Completions 流式输出，逐句 TTS 播放
- **对话历史**：可配置的上下文长度
- **自定义回复**：通过配置文件设置固定回复（文字/音频链接）
- **中断机制**：新消息到来时自动取消正在进行的 AI 回复
- **Web 管理页**：浏览器在线编辑 `config.yaml`，并发送文字到音箱测试 TTS

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

# 同时构建 chat + gemini 的 Windows 版
just build-windows

# 其他平台同理，例如 Linux arm64
just build-chat linux arm64
```

Windows 上不需要额外安装任何运行时：Go 产物是静态链接的单文件，双击或在 PowerShell 里直接运行即可：

```powershell
.\chat.exe -config config.yaml
```

> [!NOTE]
> 服务监听、配置文件路径都按 Windows 习惯工作（配置里用反斜杠或正斜杠都可以）。TTS 合成音频会写入系统临时目录（`%TEMP%\open-xiaoai-tts`）。

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

# 与 apps/gemini 同时运行：将 apps/chat 改为 4400 端口（config.yaml 中 server.port: 4400）
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
| `tts.format` | 音频格式：`mp3`（默认）/ `ogg_opus` / `pcm` / `wav` |
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
  format: "mp3"
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

工作方式：`apps/chat` 收到 AI 逐句回复后，先在服务端合成 MP3（结果缓存到系统临时目录的 `open-xiaoai-tts/`），再通过本服务新增的 `/tts/<文件名>` 路由把音频地址交给音箱播放，因此**音箱必须能访问到本服务的端口**（与本地音乐使用同一套连接感知 host）。

实现细节：该接入复用豆包网页端 `voicegenie` WebSocket，协议为手写 protobuf 帧（`StartTask → StartSession → BidirectionalTTS → EndTTS`），音频分片直接拼接。目前该端点不需要登录 Cookie，属于逆向实现，接口变动时可能需要重新适配。

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
