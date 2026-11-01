# Open-XiaoAI

把小爱音箱变成你自己的 AI 音箱。

Open-XiaoAI 通过运行在音箱上的 client 补丁程序，把小爱音箱的语音识别、音频输入、播放状态和远程执行能力交给你自己的 Server。你可以接入 Chat 类大模型，也可以让音箱播放本地音乐、网络音乐，并借助 LX Sync Server 搜索和下载歌曲。

当前重点维护的 `apps/client`、`apps/chat` 和 `pkg/music` 都是使用 Golang 重写的新版本，目标是减少运行时依赖，尽量用单二进制完成部署。

<img src="./docs/images/cover.jpg" alt="Open-XiaoAI cover" width="400">

## 你可以用它做什么

- 接入 `apps/chat`：对话后端可选 codex（能调工具）、豆包网页直连、或 OpenAI 兼容接口（GPT / Claude / DeepSeek / 通义千问）。
- 启用 `pkg/music`：用语音播放本地歌曲、故事、有声书，支持上一首、下一首、随机播放、循环模式和自动切歌。
- 播放网络歌曲：本地曲库找不到时，调用 LX Sync Server 搜索歌曲并获取播放链接。
- 下载网络歌曲：明确说“下载歌曲名”后，通过 LX Sync Server 的代理下载到本地目录，下载成功后再播放本地文件。
- 自己扩展能力：Server 端收到音箱事件后可以接任意 AI、Agent、脚本或家庭自动化逻辑。

## 项目结构

仓库按角色组织：

- `apps/`：可独立部署的二进制（`client`、`chat`），各自带 `go.mod`、`build.sh` 与 `README.md`
- `pkg/`：被 `apps/` 中模块 import 的可复用 Go 库（目前只有 `music`）
- `tools/`：一次性 / 辅助工具（刷机、固件补丁、QEMU 调试环境）

`apps/client` 是运行在小爱音箱上的 Go 客户端。它负责连接 Server、上报语音识别结果和播放状态、转发音频流，并响应 Server 发来的播放、TTS、Shell RPC 等指令。

`apps/chat` 是文本流式 + TTS 模式的 Go AI Server，单二进制部署。用户说话后，Server 将语音识别文本发送给 OpenAI 兼容 API，流式接收回答，并逐句让小爱音箱播放 TTS。它适合接入 GPT、Claude、DeepSeek、通义千问等 Chat AI，也适合做关键词触发、可打断的语音助手。

`pkg/music` 是可复用音乐模块，目前集成在 `apps/chat` 中。它可以扫描本地音乐目录，建立曲库索引，响应“播放稻香”“下一首”“随便听听”等语音指令；本地找不到时，还可以通过 LX Sync Server 搜索、播放或下载网络歌曲。

整体链路：

```text
小爱音箱 apps/client
  -> WebSocket
  -> apps/chat / 自定义 Server
  -> AI 模型、LX Sync Server、本地音乐文件、脚本或其他服务
  -> 小爱音箱播放语音或音乐
```

## 选哪种玩法

`apps/chat` 是唯一的 AI Server：对话后端可在 **codex（可调 MCP 工具）**、**豆包网页直连**、**OpenAI 兼容 API**（GPT / Claude / DeepSeek / 通义千问）之间切换；内置 21 个 MCP 工具（音乐 / 播报 / 定时任务 / 家电 / 搜索）、管理台和音乐态语音控制。

如果你主要想让小爱音箱变成本地音乐播放器，使用 `apps/chat + pkg/music`。`pkg/music` 会先搜索本地曲库；只要本地有命中，就播放本地文件，不会触发远程搜索。只有本地完全找不到普通歌曲时，才会进入 LX Sync Server 在线兜底。

## 核心能力

### apps/chat：AI 助手、音乐与家电控制

`apps/chat` 是通用 AI 助手入口，对话后端三选一：

- **codex**（推荐）：常驻 `codex app-server`，能直接调用 MCP 工具，会话可跨重启恢复
- **豆包网页直连**：免费、自带联网搜索，但没有工具调用
- **OpenAI 兼容 API**：GPT / Claude（OpenRouter）/ DeepSeek / 通义千问

它的特点：

- **MCP 工具**：音乐、播报、定时任务、家电（Home Assistant）、联网搜索，共 21 个
- **音乐态语音控制**：放歌时说「下一首」「停下」「大点声」不用喊唤醒词；点歌/换内容走三层路由（工具路由 → 分类器 → codex 兜底）
- **放歌自动降音量**：起播前压低音量让麦克风听得清人声，停播还原
- **指示灯**：正在处理你的话（识别/思考/路由）时点亮，噪声不亮
- 文本流式输出，逐句 TTS 播放，可按关键词或唤醒词打断
- 提供 Web 管理台：配置、唤醒词、路由漏斗、智能体上下文、定时任务、TTS 试播

文档：[`apps/chat/README.md`](apps/chat/README.md)

### pkg/music：本地音乐、网络音乐与下载

`pkg/music` 是 `apps/chat` 的音乐模块。启用后，你可以直接对小爱说：

```text
播放稻香
播放周杰伦
下一首
上一首
随便听听
单曲循环
全部循环
随机播放
停止播放
刷新曲库
默认故事是三国演义第一季
播放第4集
```

它支持：

- 扫描本地音乐目录，提取歌名、歌手、专辑等元数据。
- 按歌名、歌手、专辑、文件名模糊搜索。
- 通过 HTTP 文件服务把本地音频 URL 发给小爱播放。
- 监听音箱播放状态，歌曲结束后自动切下一首。
- 支持故事/有声书，比如“播放西游记第 11 集”。
- 支持默认故事上下文：先说“默认故事是三国演义第一季”，后续说“播放第4集”会自动补全到该系列。
- 支持 LX Sync Server 在线兜底。

LX Sync Server 项目地址：[XCQ0607/lxserver](https://github.com/XCQ0607/lxserver)

配置示例：

```yaml
music:
  enabled: true
  dirs:
    - /path/to/music

  lx:
    enabled: true
    base_url: "http://localhost:9527"

    # 推荐使用普通用户 token，或配置 username/password 自动登录获取 token
    user_token: ""
    username: "your_lx_user"
    password: "your_lx_password"

    # 不建议日常播放依赖 admin 口令；保留仅用于兼容
    frontend_auth: ""

    # false: 直接播放 LX 返回的远程 URL
    # true:  通过 LX 代理下载到本地后播放
     download: false            # 普通播放不下载；说“下载歌曲”时才保存
    download_dir: ""  # 空则下载到 music.dirs[0]

    # LX 音源：kw=酷我，tx=QQ音乐，wy=网易云，kg=酷狗，mg=咪咕
    source: "kw"
    # 128k / 320k / flac，取决于源和歌曲
    quality: "128k"
```

本地优先规则：

- 如果你说“播放稻香”，本地曲库能找到《稻香》，就直接播放本地文件。
- 如果你说“播放周杰伦”，本地曲库里有周杰伦的歌，就播放本地搜索到的周杰伦歌曲列表。
- 如果本地没有《稻香》，你可以说“播放周杰伦的稻香”，`pkg/music` 会调用 LX Sync Server 搜索并在线播放；说“下载周杰伦的稻香”才会下载为 `稻香 - 周杰伦.mp3` 后播放。
- 只要本地有任何命中，就不会触发 LX 远程搜索或下载。

文档：[`pkg/music/README.md`](pkg/music/README.md)

## 快速开始

> [!IMPORTANT]
> 当前教程主要面向 **小爱音箱 Pro（LX06）** 和 **Xiaomi 智能音箱 Pro（OH2P）**。其他型号请不要直接套用，刷机和补丁过程可能不同。

### 1. 准备音箱

先刷机并开启 SSH。两条路径任选：

- **直接下载预制补丁固件**（推荐普通用户）：在 [GitHub Releases](https://github.com/idootop/open-xiaoai/releases) 找到对应机型的 `xxx_patched.squashfs`，详见 [`tools/client-patch/README.md`](tools/client-patch/README.md#下载预制补丁固件)
- **自行制作补丁固件**：[`tools/client-patch/README.md`](tools/client-patch/README.md#自行制作固件)

拿到 `xxx_patched.squashfs` 后，按照刷机教程刷入并开启 SSH：[`docs/flash.md`](docs/flash.md)

### 2. 在音箱上运行 client

`apps/client` 负责连接你的 Server。下载和运行方式见：[`apps/client/README.md`](apps/client/README.md)

常见运行方式：

```shell
/data/open-xiaoai/client ws://你的server地址:4399
```

client 支持在两套 Server 之间切换（`-switch`），例如家里和异地各跑一个：

```shell
/data/open-xiaoai/client -switch ws://你的IP:4399 ws://你的IP:4400
```

文档：[`apps/client/README.md`](apps/client/README.md)

### 3. 运行 chat

到 [Releases](https://github.com/cxjava/open-xiaoai/releases/latest) 下载对应平台的 `chat_*.tar.gz`（或在 `apps/chat` 下 `bash build.sh` 自行构建）：

```shell
# 下载并解压（以 Linux x86_64 为例）
curl -L https://github.com/cxjava/open-xiaoai/releases/latest/download/chat_Linux_x86_64.tar.gz \
  | tar -xz -C /opt/open-xiaoai-chat
cd /opt/open-xiaoai-chat

# 编辑配置后启动
vim config.yaml
./chat -config config.yaml
```

默认监听 `ws://0.0.0.0:4399`。

启动后可访问 `http://你的IP:4399/admin` 打开 Web 管理页。管理页会读取并覆盖当前 `-config` 指定的 YAML 文件；如需暴露到局域网之外，请先在 `auth.users` 配置 Basic Auth。

文档：[`apps/chat/README.md`](apps/chat/README.md)

### 5. 启用 pkg/music

`pkg/music` 通过 `apps/chat` 的 `config.yaml` 启用：

```yaml
music:
  enabled: true
  dirs:
    - /path/to/music
```

如果要播放和下载网络歌曲，再配置 `music.lx`，并单独启动 LX Sync Server。

## 相关项目

如果你不想刷机，或者设备型号不适合本项目，可以参考：

- [idootop/mi-gpt](https://github.com/idootop/mi-gpt)
- [idootop/migpt-next](https://github.com/idootop/migpt-next)
- [yihong0618/xiaogpt](https://github.com/yihong0618/xiaogpt)
- [hanxi/xiaomusic](https://github.com/hanxi/xiaomusic)
- [XCQ0607/lxserver](https://github.com/XCQ0607/lxserver)

## 参考链接

- [小爱音箱相关研究记录](https://github.com/yihong0618/gitblog/issues/258)
- [jialeicui/open-lx01](https://github.com/jialeicui/open-lx01)
- [duhow/xiaoai-patch](https://github.com/duhow/xiaoai-patch)
- [javabin.cn 小爱相关记录](https://javabin.cn/2021/xiaoai_fm.html)
- [xuanxuanblingbling IoT 文章](https://xuanxuanblingbling.github.io/iot/2022/09/16/mi/)

## 免责声明

1. **适用范围**  
   本项目为开源非营利项目，仅供学术研究或个人测试用途。严禁用于商业服务、网络攻击、数据窃取、系统破坏等违反《网络安全法》及使用者所在地司法管辖区法律规定的场景。

2. **非官方声明**  
   本项目由第三方开发者独立开发，与小米集团及其关联方无任何隶属或合作关系，亦未获其官方授权、认可或技术支持。项目中涉及的商标、固件、云服务的所有权利归属小米集团。若权利方主张权益，使用者应立即主动停止使用并删除本项目。

继续下载或运行本项目，即表示你已完整阅读并同意[用户协议](agreement.md)，否则请立即终止使用并彻底删除本项目。

## License

MIT License
