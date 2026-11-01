# 部署指南

本文覆盖从刷机到语音助手跑起来的完整流程：刷机、音箱端 client、服务端（Windows）、ASR 模型、Home Assistant 家电控制、管理台与账号登录。

> `<windows-host>`、`<speaker-ip>`、`<ha-ip>`、`E:\open-xiaoai` 均为示例，请按实际环境替换。
> 涉及账号/令牌的操作只写流程，请勿把任何密码、token、cookie 提交到仓库。

## 前置条件

- 一台小爱音箱（本项目实测 OH2P；其他型号需确认固件与分区）
- 一台常开的 Linux / Windows / macOS 主机跑 `apps/chat`（本文以 Windows 为例）
- 音箱与主机在同一局域网；主机能访问互联网（模型/TTS/搜索）
- 刷机需要一台 Windows 电脑（见第 1 节）

## 0. 环境速查

| 角色 | 地址 | 说明 |
|---|---|---|
| 开发机 | 仓库目录 | 交叉编译、改前端、构建固件 |
| Windows 主机 | `<windows-host>`（SSH 别名/地址） | 生产服务，示例目录 `E:\open-xiaoai`，计划任务 `openxiaoai` |
| 小爱音箱 OH2P | `<speaker-ip>` | SSH `root`，密码在 `tools/client-patch/.env`（不入库） |
| Home Assistant | `<ha-ip>:8123` | Hyper-V 虚拟机，HAOS + HA |
| HAOS 调试 SSH | `ssh -p 22222 root@<ha-ip>` | 可跑 `ha` CLI / `docker` |

```text
音箱 client（麦克风 PCM / 播报 RPC）
  → WebSocket 4399 → apps/chat
    → VAD + ASR → 唤醒词/对话路由 → codex app-server
      → MCP 工具（音箱播放 / 定时任务 / 家电 / 搜索）
        → 音箱 TTS
        → Home Assistant REST → 家电
```

---

## 1. 刷机（优先在 Windows 上做）

**为什么必须 Windows**：macOS 的 AppleUSBHub 会独占设备，永远抓不到 U-Boot 窗口；实测在 Windows 上一次成功。

### 1.1 准备固件

两种来源：

1. 下载上游 Releases 里对应型号、**同版本**的 `*_patched.squashfs`；
2. 自己制作（`tools/client-patch`）：OTA 取包 → `src/extract.sh` → `src/patch.sh` → `src/squashfs.sh`，产物在 `tools/client-patch/assets/<型号_版本>/root-patched.squashfs`。

制作固件的坑（详见 `tools/client-patch/README.md` 与 `docs/flash.md`）：

- OTA 接口现在只返回 `data.upgradeInfo`（下一版），不再返回当前版本信息；需要手动下载 `mico_all_<hash>_<ver>.bin` 全量镜像再提取。
- `extract.py` 会校验文件名 hash，重命名过要加 `-i`。
- `MI_DEBUG` 在 logger 初始化时读取，必须先 `set -a && . ./.env && set +a`。
- 重打包前 `rm -rf assets/<fw>`，否则 `cp -rf` 会嵌套目录。
- **`mksquashfs` 必须补回 `/dev/console` 字符设备**：`-p '/dev/console c 600 0 0 5 1'`，否则刷完可能 SSH 不通。

### 1.2 Windows 刷机工具

仓库只内置了 macOS 版 `tools/flash`。Windows 侧使用同源的 Amlogic 命令行工具（`update.exe`，来自 [khadas/utils / aml-flash-tool](https://github.com/khadas/utils/tree/master/aml-flash-tool)）；刷机是在一台 Windows 机器上实测完成的，目录结构一般：

```text
update.exe
bin\
drivers\InstallDriver_Worldcup.exe
drivers\x86\libusb0_x86.dll
```

踩过的坑：

- `update.exe` 报 `0xC0000135`（DLL not found）：把 `drivers\x86\libusb0_x86.dll` 复制成 `bin\libusb0.dll`（并放入 `SysWOW64`）。
- 驱动用 `InstallDriver_Worldcup.exe` 手动以管理员运行；`pnputil /enum-drivers` 能看到 `worldcup_device.inf` 即成功。
- 在 SSH（Session 0）里 `start /b`、`Start-Process` 拉不起后台脚本 → 用 `schtasks /create ... /ru SYSTEM` + `schtasks /run`。
- Windows 下 `.bat` 必须 **CRLF**，LF 会让 `goto` 标签失效、脚本静默不动。

### 1.3 刷写步骤

```bat
:: 1) 抓 U-Boot 窗口：先拔电源，再插上，反复重试直到打印 firmware version
update.exe identify

:: 2) 启动延时 15 秒（给刷写留时间）
update.exe bootdelay 15

:: 3) 切到 boot0
update.exe boot_part boot0

:: 4) 写入 system0（约 8 秒写完 30MB）
update.exe partition system0 <root-patched.squashfs 的绝对路径>
```

> 子命令以 `update.exe help` 为准，上面是实测顺序。
> 成功标志：`[update]:mwrite success`。之后拔数据线和电源，重新上电。

**回滚**：把启动分区切回 `boot1` 可恢复原系统。跨版本刷机有变砖风险，务必用设备当前版本对应的固件。

---

## 2. 音箱端 client 部署

刷机后的系统带 SSH（密码来自 `.env` 的 `SSH_PASSWORD`）。dropbear 很老，连接参数：

```bash
ssh -o IPQoS=none -o HostKeyAlgorithms=ssh-rsa -o PubkeyAcceptedAlgorithms=ssh-rsa \
    -o KexAlgorithms=curve25519-sha256@libssh.org,diffie-hellman-group14-sha1 root@<speaker-ip>
```

目录结构：

```text
/data/init.sh                 → exec /data/open-xiaoai/boot.sh
/data/open-xiaoai/
  boot.sh                     等网络就绪后拉起 init.sh
  init.sh                     守护进程：单实例锁 / 崩溃重启 / status / stop / upgrade
  client                      linux/arm/v7 静态二进制
  server.txt                  启动参数
```

`server.txt` 的 **flag 必须写在地址前面**（Go `flag.Parse` 遇到第一个非 flag 参数就停止）：

```text
-mic-stream ws://<windows-host>:4399
```

部署/升级三件套（少一步就会出现“改了没生效”）：

```bash
# 传输建议：Mac 上起 python3 -m http.server，音箱 wget 拉（比 scp 稳）
wget -O /data/open-xiaoai/client.new http://<mac-ip>:8000/client
/data/open-xiaoai/init.sh stop
cp /data/open-xiaoai/client.new /data/open-xiaoai/client
/data/open-xiaoai/init.sh daemon
```

改过 `server.txt` 后同样需要 `stop` + `daemon`，`upgrade` 会沿用旧参数。

麦克风上行要点（服务端唤醒链路）：

- 固件用 ALSA dsnoop，设备名是 `/etc/asound.conf` 里的 `noop`，不是 `hw:0,3`；`arecord -D noop` 才能和固件同时收音。
- PDM 数据在 S32_LE 的低 24 位：抓 4ch/48k/S32，四通道平均后 `>> 8` 再转 int16，然后 48k→16k。
- 环境底噪高（RMS≈2000），VAD 必须自适应：噪声基线慢跟踪 + 门限 `max(1200, noise*2.2)`；开口要预录 500ms，否则吃字头。

---

## 3. chat 服务部署（Windows）

### 3.1 目录

```text
E:\open-xiaoai\
  chat.exe                windows/amd64 静态二进制
  config.yaml             全部用 Windows 绝对路径
  run.bat                 cd /d E:\open-xiaoai && chat.exe -config ... >> chat.log
  chat.log                UTF-8 运行日志
  asr_worker.py           ASR worker
  asr-venv\               Python venv（sherpa-onnx）
  models\sensevoice\      SenseVoice int8 模型
  models\paraformer\      Paraformer（唤醒兜底）
  doubao-session.json     豆包登录态（gitignored）
  netease-session.json    网易云登录态（gitignored）
  .codex-deepseek\        codex 配置目录
  web\                    管理台静态文件（index.html + assets）
```

### 3.2 编译与发布

```bash
# 本地先编译验证
cd apps/chat && go build ./... && go test ./...

# 交叉编译
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o ../../dist/chat_windows_amd64.exe .

# 更新顺序（运行中的 exe 被锁，先覆盖会静默失败）
scp ../../dist/chat_windows_amd64.exe win-host:E:/open-xiaoai/chat.new.exe
ssh win-host 'taskkill /IM chat.exe /F'
ssh win-host 'move /y E:\open-xiaoai\chat.new.exe E:\open-xiaoai\chat.exe'
ssh win-host 'schtasks /run /tn openxiaoai'
```

覆盖后核对文件大小/时间戳，再怀疑逻辑。前端：

```bash
cd apps/chat/web && pnpm install && pnpm build
scp dist/index.html win-host:E:/open-xiaoai/web/
scp -r dist/assets win-host:E:/open-xiaoai/web/
```

### 3.3 首次安装 / 开机自启

```bat
schtasks /create /tn openxiaoai /tr "E:\open-xiaoai\run.bat" /sc onstart /delay 0000:30 /ru SYSTEM /rl HIGHEST /f
netsh advfirewall firewall add rule name="openxiaoai-4399" dir=in action=allow protocol=TCP localport=4399
```

`schtasks` 不设置工作目录，所以 `-config`、`session_file`、`model_dir` 等**全部写绝对路径**；`run.bat` 必须 CRLF。

### 3.4 配置要点（`apps/chat/config.yaml`）

- `chat.provider`: `codex`（推荐，能调 MCP 工具）/ `doubao_web` / `llm`
- `codex.bin / workspace / codex_home / timeout_seconds`
- `prompt.system`: 语音场景提示词（会被 TTS 念出来，保持短、口语）
- `asr.*`: `wake_provider`、`dialog_provider`、模型目录、火山 API Key
- `tts.provider`: `xiaoai` / `doubao`
- `homeassistant.base_url` + `token`: 家电控制
- `continuation_seconds`: 对话窗口时长（默认 20）

### 3.5 codex / MCP

`E:\open-xiaoai\.codex-deepseek\config.toml` 关键段（不要把真实 key 写进仓库）：

```toml
model = "deepseek-flash"
model_reasoning_effort = "medium"
web_search = "disabled"

[mcp_servers.openxiaoai]
url = "http://127.0.0.1:4399/mcp"
default_tools_approval_mode = "approve"
```

MCP 服务端由 chat 自己提供（`/mcp`，仅本机）。工具：音乐 7 个、播报、定时任务 7 个、家电 2 个、搜索、时间、结束对话。
`prompt.system` 是在 `thread/start` 时作为 `baseInstructions` 传给 codex 的，**改完必须重启服务**才会新建 thread 生效。

### 3.6 macOS 部署（最简单）

macOS 只跑服务端，不参与刷机（刷机必须用 Windows）。Apple Silicon / Intel 都可以，ASR 走 CPU。

依赖：Go 1.27+、Node + pnpm、Python 3.10+。

```bash
# 1) 编译服务端
cd <repo>/apps/chat
go build -o chat .

# 2) 准备部署目录（config 与 web 放同一层）
mkdir -p ~/open-xiaoai/web
cp chat ~/open-xiaoai/
cp config.yaml ~/open-xiaoai/
cp -r web/dist/* ~/open-xiaoai/web/          # 管理台静态文件

# 3) 启动
cd ~/open-xiaoai
./chat -config ~/open-xiaoai/config.yaml
```

`config.yaml` 里需要改成本机绝对路径（**相对路径会失效**）：

```yaml
server:
  host: "0.0.0.0"
  port: 4399

asr:
  enabled: true
  python: "/Users/<you>/.venvs/sherpa/bin/python"
  script: "/Users/<you>/open-xiaoai/tools/asr-worker/asr_worker.py"
  model_dir: "/Users/<you>/open-xiaoai/models/sensevoice"
  alt_model_dir: "/Users/<you>/open-xiaoai/models/paraformer"
```

ASR 环境（模型文件要求见第 4 节）：

```bash
python3 -m venv ~/.venvs/sherpa
~/.venvs/sherpa/bin/pip install -U sherpa-onnx numpy

# SenseVoice int8：model.int8.onnx + tokens.txt  → models/sensevoice/
# Paraformer：model.onnx + tokens.txt             → models/paraformer/
# 模型可从 HuggingFace 下载；国内可用 hf-mirror.com 镜像
```

常驻运行建议用 launchd（用户级，开机自启）：

```xml
<!-- ~/Library/LaunchAgents/com.openxiaoai.chat.plist -->
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.openxiaoai.chat</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/&lt;you&gt;/open-xiaoai/chat</string>
    <string>-config</string>
    <string>/Users/&lt;you&gt;/open-xiaoai/config.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/&lt;you&gt;/open-xiaoai</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/&lt;you&gt;/open-xiaoai/chat.log</string>
  <key>StandardErrorPath</key><string>/Users/&lt;you&gt;/open-xiaoai/chat.log</string>
</dict>
</plist>
```

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.openxiaoai.chat.plist
# 或者旧写法：launchctl load -w ~/Library/LaunchAgents/com.openxiaoai.chat.plist
```

其它注意事项：

- 音箱的 `server.txt` 指向 Mac 的局域网地址：`-mic-stream ws://<mac-lan-ip>:4399`；
- macOS 防火墙弹窗要允许 `chat` 接受传入连接；
- Mac 睡眠会断连，建议关闭休眠或用 `caffeinate -dimsu` 包一层；
- macOS 能解析 `homeassistant.local`，Home Assistant 的 `base_url` 可以直接写 mDNS 名（Windows 则需要 IP）；
- 豆包 / 网易云登录态、`config.yaml` 都在部署目录，**不要提交**。

---

## 4. ASR 模型部署

### 4.1 模型与 worker

- 引擎：sherpa-onnx（Python 包）
- 主模型：SenseVoice int8（`model.int8.onnx` + `tokens.txt`，约 228MB）
- 备选：Paraformer-zh（约 784MB），只在唤醒兜底 / `local_alt` 时用
- worker：`tools/asr-worker/asr_worker.py`，常驻子进程，stdin/stdout 传 WAV 路径与结果

安装（Windows 示例）：

```bat
uv venv E:\open-xiaoai\asr-venv
uv pip install --python E:\open-xiaoai\asr-venv\Scripts\python.exe sherpa-onnx numpy
```

配置：

```yaml
asr:
  enabled: true
  wake_provider: "local"          # 唤醒：SenseVoice（快）+ Paraformer 兜底
  dialog_provider: "local_alt"    # 对话：可选 local_alt / local / doubao_stream / doubao
  python: "E:\\open-xiaoai\\asr-venv\\Scripts\\python.exe"
  script: "E:\\open-xiaoai\\asr_worker.py"
  model_dir: "E:\\open-xiaoai\\models\\sensevoice"
  alt_model_dir: "E:\\open-xiaoai\\models\\paraformer"
```

坑：

- **Windows 上 Python 默认 GBK 输出，Go 按 UTF-8 解析会乱码**，表现为「叫了没反应」。`asr_worker.py` 必须 `sys.stdout.reconfigure(encoding="utf-8")`。
- 排查 ASR 时别 `taskkill /F /IM python.exe`（会杀掉机器上所有 Python），按 PID 或命令行精确杀。
- 唤醒和对话识别是分开的：唤醒永远先走 SenseVoice，空文本/没命中才用 Paraformer 兜底；对话识别按 `dialog_provider` 走。

---

## 5. Home Assistant（Windows / Hyper-V）

### 5.1 虚拟机

- Hyper-V 虚拟机名 `HomeAssistant`，Gen2，磁盘 `E:\ha\haos_ova-18.4.vhdx`，外部交换机
- 启动：`powershell -Command "Start-VM -Name HomeAssistant"`，IP `<ha-ip>`（建议在路由器做静态租约）
- 调试 SSH：`ssh -p 22222 root@<ha-ip>`；宿主机看到配置目录是 `/mnt/data/supervisor/homeassistant`

### 5.2 装 Core（镜像拉不动时）

Supervisor 直连 ghcr.io 只有 ~70KB/s，会卡在 `version: landingpage`。绕过：

```bash
# Mac（arm64 要指定 amd64）
docker pull --platform linux/amd64 ghcr.nju.edu.cn/home-assistant/qemux86-64-homeassistant:<版本>
docker tag ghcr.nju.edu.cn/... ghcr.io/home-assistant/qemux86-64-homeassistant:<版本>
docker save ghcr.io/... | gzip -1 | ssh -p 22222 root@<ha-ip> "zcat | docker load"
# HAOS 里
ha core start
```

### 5.3 端口必须是 8123

小米 OAuth 回调白名单只认 `http://homeassistant.local:8123`。HA 的 HTTP 集成把端口存在 `.storage/http` 的 `data.stable.server_port`，`ha core options --port` 会被它覆盖：

```bash
F=/mnt/data/supervisor/homeassistant/.storage/http
cp "$F" "$F.bak"
jq '.data.stable.server_port = 8123' "$F.bak" > "$F"
ha core restart
```

### 5.4 Xiaomi Home 集成

- `custom_components/xiaomi_home`（v0.5.0+）已放进配置目录，重启 HA 后可在「设置 → 设备与服务 → 添加集成」里搜到
- 用米家/小米账号扫码或密码登录，授权后回调 `homeassistant.local:8123`
- 集成配置项 `ctrl_mode: auto`：设备支持局域网 miot 且同网段时走本地，否则走小米云
- 这是官方 OAuth 通道（`ha.api.io.mi.com`），和之前第三方签名直连遇到的 `429 client withdraw` 不是一回事

### 5.5 给 chat 用的长期访问令牌

HA → 左下角头像 → 个人资料 → 安全 → 长期访问令牌 → 创建，然后把 token 填进 chat 配置（**只填远端 `E:\open-xiaoai\config.yaml`，不要提交**）：

```yaml
homeassistant:
  base_url: "http://<ha-ip>:8123"   # Windows 解析不了 .local，用 IP
  token: "<长期访问令牌>"
```

YAML 缩进必须是 `homeassistant:` 下两空格；粘贴丢缩进会变成顶层 `token:`，静默失效。

---

## 6. 账号登录（都在管理台完成）

管理台：`http://<windows-host>:4399/ui/`

| 账号 | 入口 | 落盘文件 | 备注 |
|---|---|---|---|
| 豆包（会话/定时任务） | 应用管理 → 豆包 → 扫码登录 | `doubao-session.json` | 用豆包 App 扫码 |
| 网易云音乐 | 应用管理 → 网易云音乐 → 扫码登录 | `netease-session.json` | 未登录只能播免费曲目 |
| Home Assistant | 配置 → 家电 → 长期访问令牌 | `config.yaml`（远端） | 见 5.5 |

以上 session 文件都在 `.gitignore` 里，**绝不要提交**。

---

## 7. 管理台

- 地址 `http://<server>:4399/ui/`（前端是静态文件，可单独部署；API 同源）
- 页面：豆包（会话记录）/ 应用管理（扫码登录）/ 定时任务 / 唤醒与识别 / 配置 / 语音测试
- 「唤醒与识别」：自定义唤醒词、在线测试、引擎切换（热生效）
- 「配置」：表单 / YAML 双视图，左侧分区；密码类字段不回显，留空保持不变
- 登录认证由 `auth.users` 控制；为空表示不校验（仅内网使用）

调试接口（仅 loopback）：

| 接口 | 用途 |
|---|---|
| `POST /admin/api/debug/shell` | 在音箱上执行 shell |
| `POST /admin/api/debug/utterance` | 模拟一句语音输入 |
| `POST /admin/api/debug/doubao-chat` | 直接给豆包发一句话（收集语料/排查） |
| `POST /admin/api/debug/doubao-raw` | 同上，额外返回原始 SSE |
| `POST /admin/api/debug/doubao-reset` | 重置豆包会话 |

---

## 8. 常见坑速查

| 现象 | 原因 / 处理 |
|---|---|
| 改了代码没生效 | 远端 exe 被锁，scp 静默失败；先 `taskkill` 再覆盖，核对大小 |
| 服务启动秒退 | `schtasks` 无工作目录；`-config` 和所有路径写绝对路径 |
| 局域网连不上 4399 | Windows 防火墙未放行 |
| `.bat` 不执行 | 行尾是 LF，必须 CRLF |
| 叫了没反应 / ASR 乱码 | Python 输出 GBK；worker 强制 UTF-8 |
| 音箱和小爱同时说话 | 未调 `stop_native`；客户端需带压制窗口 |
| 刷完 SSH 不通 | `mksquashfs` 丢了 `/dev/console` |
| 刷机抓不到设备 | macOS 抓不到 U-Boot，换 Windows + 驱动 + libdll 修复 |
| HA 卡在 landingpage | ghcr 太慢；用镜像源预拉 + `docker load` |
| HA 端口是 80 | `.storage/http` 的 `server_port` 未改回 8123 |
| 小米 429 client withdraw | 第三方签名直连被风控；改走 HA（官方 OAuth），不要高频重试 |
| 配置保存不生效 | 部分字段需要重启（server/proxy/LLM、ASR 基础设施、codex 路径）；提示词需重启 codex thread |

---

## 9. 变更流程与红线

1. **先本地验证再部署**：`go build ./... && go test ./...`，前端 `pnpm build`。
2. 只合并配置变更块，**不要跨平台整份覆盖 config.yaml**（Mac 路径会覆盖 Windows 路径，ASR 会挂）。
3. 更新二进制用 `taskkill → 覆盖 → schtasks /run`；更新前端只传 `index.html + assets`。
4. **不要把密码、token、cookie、session 文件写进仓库或文档**；`.env`、`*-session.json` 都已 gitignore。
5. 本项目不提供 Docker 镜像：`apps/chat` 以单二进制部署，保持零运行时依赖。
6. 排查 ASR 时不要杀掉所有 python 进程。
7. 家电控制统一走 Home Assistant；不建议引入 `api.io.mi.com` 的第三方签名直连（易触发风控）。
8. 改 `prompt.system` 后要重启服务，codex 才会用新的 baseInstructions。

---

## 10. 参考

- `docs/flash.md`：原始刷机教程（macOS 工具）
- `tools/flash/README.md`：macOS 刷机工具说明
- `tools/client-patch/README.md`：固件制作
- `apps/chat/README.md`：服务端配置项
