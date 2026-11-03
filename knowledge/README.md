# Agent 本地知识库

当前收录四本李笑来公开书稿。入口是 [INDEX.md](INDEX.md)，原文、来源和主题索引互相分开。

| 资料 | 固定版本 | 内容 |
|---|---|---|
| 把时间当作朋友 | 第三版 `ba8305dc` | 11 个 Markdown，87 个原始文件 |
| 我也有话要说：普通人的当众讲话技能 | `b77a015b` | 40 章正文 |
| 自学是门手艺 | `987a8e8a` | 49 个 Markdown 章节 |
| 定投改变命运（第三版） | `c125a792` | 中文完整版 |

准备本地副本：

```bash
python3 tools/knowledge/fetch_time_as_a_friend.py
```

也可以使用从作者仓库下载的固定版本 ZIP：

```bash
python3 tools/knowledge/fetch_time_as_a_friend.py --archive /absolute/path/to/source.zip
```

脚本按 [source.json](books/time-as-a-friend/source.json) 核对全部 87 个原文件的大小与 Git blob SHA1，再原样安装；已有原文目录时不覆盖。Python 只用于准备资料，不是语音服务运行时新增依赖。

其余三本公开书稿从固定提交下载，只保存参与检索的 Markdown 正文：

```bash
python3 tools/knowledge/import_public_books.py
```

原始正文、序言、72 张图片和 3 份 PDF 保存在 `books/time-as-a-friend/original/`，维持作者文件结构和独立的 CC BY-NC-ND 3.0 US 许可。原文件已 gitignore；重建环境时重新下载，不把全书混入项目代码的许可。保存了副本也不能证明模型采用过它训练。

运行时通过专用的只读 MCP 服务接入，使用现有 Codex 的 Node 环境，无第三方 npm 包、向量服务或 Docker 依赖。`registry.json` 登记多份资料、启用状态、已发布版本与 generation；Go 管理台负责写入，Node worker 只读。正文按标题切分；中文双字词检索加少量主题同义词，读取结果附资料、版本和文件位置。`search_book` 定位后由 `read_book_section` 返回原文；每次最多 60 行、4000 字符，可按 `next_offset` 翻页。图片、PDF 完整保存，但当前检索只覆盖 Markdown/TXT，不宣称能检索图片或 PDF 内文。

```bash
node tools/knowledge/server.cjs --root /absolute/path/to/knowledge
node --test tools/knowledge/server.test.cjs
```

这是独立的资料读取 MCP worker；音箱操作等业务工具仍在 `apps/chat/mcp.go`。工具只读清单内、通过大小与哈希校验的原文，不接受任意文件路径、不执行命令、不联网。每次工具调用前检查 registry generation；停用资料会在检索和读取层被强制拒绝，单个新版本加载失败时保留上次有效资料。worker 心跳写入 `.runtime/worker-status.json`，管理台据此确认 agent 实际加载的 generation。

`--request` 模式只供管理端试搜/读取调用，与 MCP 共用同一套加载、切分、检索和读取代码：

```bash
printf '%s' '{"action":"search","query":"记录开销 第三组练习","limit":3}' \
  | node tools/knowledge/server.cjs --root knowledge --request
```

## 部署到 Windows agent

将资料目录放到实际路径，并将 `tools/knowledge/server.cjs` 复制为知识库根目录的 `server.cjs`。chat 的 `knowledge.root` / `knowledge.script` 和 Codex MCP 必须指向同一份 registry 与 worker；在服务实际使用的 `CODEX_HOME/config.toml` 中仅增加如下独立节（路径按环境修改，不覆盖其他配置和凭据）：

```toml
[mcp_servers.knowledge]
command = 'C:\Program Files\nodejs\node.exe'
args = ['E:\open-xiaoai\voice\knowledge\server.cjs', '--root', 'E:\open-xiaoai\voice\knowledge']
enabled_tools = ["search_book", "read_book_section"]
default_tools_approval_mode = "approve"
```

保持仓库 `prompt.system` 的知识库查询规则；更新生产配置时只替换这一字段，备份原配置、Codex TOML 和主 thread 指针，再重启服务并创建新主 thread，详见 [部署指南](../docs/deploy.md)。资料更新必须重新核对来源版本与哈希，不直接修改原书。原始下载文件不入 Git；重装时运行下载脚本。

管理台 `/ui/#knowledge` 的「书架」支持多本书和每本书多个 UTF-8 Markdown/TXT 章节文件。导入校验后默认直接发布，也可以停用或在版本记录里切回旧版；来源、许可证都不是必填，适合只在家里使用的个人资料。「检索试验」调用同一个 worker；「使用规则」只读展示当前系统提示词的边界，避免出现第二份规则编辑器。发布后资料更新走 generation 快照，不要求重建会话。

普通闲聊、倾诉和故事不强制检索。需要书中具体方法、出处、原话或适合借鉴本书的学习/时间/沟通求助时才查；检索失败不能声称已查到，引用须与实际读到的文字对应。日常回答保留口语风格，不朗读工具名、文件路径、命令或检索日志。

聊天中的应用规则见 [把知识自然用进聊天](../docs/conversational-learning-design.md)：围绕眼前卡点给一个小尝试，按用户主动反馈调整，尊重拒绝，不把倾诉变成说教。承接仅依赖当前可见会话，没有独立的长期学习记忆或自动监督能力。
