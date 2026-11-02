# Design

## Context

见 `proposal.md` 的 Why。当前分支 `fix/native-tts-suppress` 的候选方案已部署复测，但原生唤醒完整流程尚未通过：3 秒补停仍漏原生 TTS，重启 aivs 的实验影响音频并已回退。本轮音乐态续听修正与实验性指令隔离已于 23:20 部署；原生 TTS 已有人耳静音确认，但音乐无缝仍在复测。

设备上已经确认的事实：

- 原生小爱 TTS 由 `mico_aivs_lab` 发起，调用 `mediaplayer.player_play_url`，参数带 `media:"mibrain"`，URL 是 `https://speech.ai.xiaomi.com/speech/1.0/tts_token?...`。
- 网易云音乐走 `mediaplayer.player_play_url`，不带 `media`（默认/common）。
- 早期静音实验里 `stop media:"mibrain"` 后音乐 status 为 1，仅说明那次状态读数；不能证明 mibrain/common 的播放器状态严格隔离，更不能证明后续没有原生 TTS/播放动作。
- `player_get_context.is_tts` 在两种播放里都可能为 1，不能作为区分依据。
- 设备进程名：`mico_aivs_lab`（原生助手）、`mibrain_service`（ubus 名字是 `mibrain`）、`mediaplayer`、`mipns-xiaomi`。
- 豆包 TTS 走服务端 `/tts/<hash>.<format>`：当前 WAV 默认已使用 `wget | aplay -D notify`；mp3 兼容路径为 `miplayer -f`。
- 音量复测更正（2026-11-02）：`mysoftvol` 会影响音乐听感，调整时 mediaplayer 报告的 volume 可以保持不变；不能据此声称音乐音量不变。按本机实测将其作为影响音乐的全局输出增益管理，保持固定，不作为 TTS 音量旋钮。
- `notify + notifyvol` 的短音对照已有人耳确认：响度变化、背景音乐不变；0/255 极值复测也听到响度变化。目标音量分工为 TTS=`notifyvol`、音乐=`mediaplayer volume`，真实豆包 TTS 与音乐 ducking 仍待验收。这不表示所有 PCM 都经过 `mysoftvol`，也不表示 `notifyvol` 只影响豆包：其他 `notify` PCM 使用者会共享此控制。
- 原生 dialog/续听期间观察过 `status=0` 和 `status=2`；本次 22:29 日志是自动续听之后进入暂停，不能由缺少 explicit stop/pause 就断言暂停责任完全属于原生。

约束：

- 单二进制部署，不引入 Docker 运行时依赖。
- 原生 ASR 仍由 mips/aivs 提供，不能为了屏蔽 TTS 把 ASR 一起关掉。
- 不直接覆盖 main 的稳定冻结方案；分支验收后再决定合并。
- 设备是 32 位 ARM、老固件；本机已有 Zig。参考项目的 ASR-only/指令过滤钩子需要核对当前固件的动态符号、ABI 和 dialog 归属，不能直接使用其其他型号二进制。

## Goals / Non-Goals

**Goals**

- 原生唤醒接管期间绝不播放小爱自己的 TTS。
- 音乐尽量继续播放；至少不能因为原生 TTS 队列被清空而停音乐。
- 接管生命周期覆盖整轮回复，不被单句 TTS 或旧轮次收尾提前结束。
- 豆包 TTS 与音乐的音量可以独立控制。
- 每个候选方案都有实测记录，最终方案只从实测通过的方案中选。

**Non-Goals**

- 不把原生 ASR 换成我们自己的 ASR（本轮不讨论）。
- 不用小爱云端 TTS 合成豆包的回答。
- 不为“屏蔽 TTS”引入网络层拦截作为长期方案。
- 不做跨型号通用注入框架；只针对当前 OH2P 固件实现可回滚的实验性指令过滤。

## Decisions

### D1. TTS 屏蔽：冻结 + mibrain stop 仍是未通过验收的候选

原生 TTS 的请求带 `media:"mibrain"`。当前代码在接管开始和迟到 `Speak` 时请求 stop，但不能把 media 名称当作严格隔离保证。22:29:52 解冻后仍出现 `SpeechSynthesizer.Speak`、`AudioPlayer.Play`，用户确认漏音；反复 stop 是事后补救，尚未满足“零原生 TTS”。

冻结 `mico_aivs_lab` 只是暂停进程：解冻会继续处理排队指令，不等于取消云端回答。重启 aivs 已实测影响音频，淘汰为正常回合结束手段。后续优先验证按 dialog 的 ASR-only/下行指令过滤，覆盖 TTS、播放动作和音量动作；工具链与符号存在不等于已经可用。

**为什么不是只冻 aivs**：final 之后 TTS 请求可能已经发出；只冻 aivs 抢不过这个窗口。
**为什么不是只 stop mibrain**：aivs 可能继续生成下一段 TTS，需要反复清；冻结能从源头减少生成。

### D2. 接管生命周期：整轮回复，epoch + turnSeq

原来的代码把“结束接管”挂在每句 TTS 的 `markSpeaking(false)` 上，第一句播完就解冻，后续句子播放时小爱可以插话。现在改为：

- `handleMessage` 返回（整轮回复播完）才结束接管。
- `epoch` 标识第几次接管，旧的超时定时器/看门狗不能结束新接管。
- `turnSeq` 标识第几轮回复，旧轮次的协程不能结束新轮次的接管。

### D3. 豆包 TTS 播放路径：wav + aplay 优先，mp3 + miplayer 兼容

- `wav`：目标改为 `wget | aplay -D notify`，走 `notifyvol → dmixer`，与 mediaplayer 的音乐叠播；`Playback + mysoftvol` 因调音量会连带改变音乐而淘汰为 TTS 独立音量方案。
- `mp3`：`miplayer -f`，会占用音频设备，播放前需要 `mphelper pause` 暂停音乐。
- 代码目前用 URL 后缀判断；后续改为显式配置 `tts.player: aplay|miplayer`，不靠后缀猜。

本地唤醒「豆包豆包」已经能观察到旧 WAV/aplay 路径与音乐共存；本次 `aplay -D notify` 短音也返回成功，前后音乐 status 保持 1。真实豆包 TTS 的 notify 路由、首声延迟和音量隔离仍待验证；原生唤醒开麦停音乐是另外需要解决的环节。

### D4. 音乐状态：status=0 是中断，不是播完

`trackMusic` 原来把 `status=0` 和 `status=3` 都当自然播完，导致原生 dialog 一停播放器就自动切歌。现在只有 `status=3` 才接下一首；`status=0` 视为外部中断，不切歌，持续空闲 30 秒才退出音乐态。回合结束优先 `mphelper play` 原位置恢复，失败才重拉 URL。

22:29 复测另发现：回答结束后主动 `native_continue_dialog`，未登记收音窗口；随后播放器为 `status=2`，tracker 退出音乐态。本轮本地修正为音乐态不自动原生续听，下一问需再喊小爱同学；非音乐态先开 ASR 窗口，匹配 dialog 的空 final/Finish 或 8 秒超时关窗，旧窗口的超时不得关闭新窗口。此修正不代表迟到原生 TTS 已解决。

### D5. 音量：notifyvol 为保留方案，mysoftvol 保持固定

- **淘汰的旧方案**：`Playback + mysoftvol`。实测调节 `mysoftvol` 时音乐听感改变，即使 mediaplayer 的 volume 不变，不能实现 TTS 与音乐独立调节。
- **方案 A（保留，待真实 TTS 验收）**：`set_tts_volume` MCP 工具 + `tts.volume` 配置映射到 `amixer -c 0 sset notifyvol 0-255`，豆包 WAV 使用 `aplay -D notify`。短音对照已验证 TTS 通道响度变化且音乐听感不变；`mysoftvol` 保持固定。`notifyvol` 对共享 notify PCM 的其他声音也生效，不宣称严格只影响豆包。
- **方案 B**：服务端 TTS 增益或豆包 TTS 的 `volume` 参数。只影响豆包 TTS，但需要改合成/缓存链路，并确认接口支持。
- **方案 C（补充实验）**：`player_set_volume` 的 `media` 参数是否能只控 common/音乐。

方案 A 先用相同真实豆包 WAV 做 `notifyvol=60/180` 对照，同时记录 mixer、音乐状态和人耳反馈；再验证音乐 ducking 时 TTS 不变、重启后配置恢复。方案 B 仍保留评估任务。mediaplayer 的 volume 字段不变不能单独作为音乐听感不变的证据。

### D6. 候选方案全部实测，URL 劫持只做兜底实验

所有讨论过的路线都要有结果记录，不能因为“看起来麻烦”直接跳过：

| # | 方案 | 目的 | 当前状态 | 预期/结论 |
|---|---|---|---|---|
| 1 | 冻结 `mediaplayer`（main 基线） | 挡住原生 TTS | 已验证 | 能挡住，但音乐一起停；不作为共存方案 |
| 2 | 冻结 `mico_aivs_lab` | 让 aivs 生成不了 TTS | 已验证 | 音乐能继续，但 TTS 请求有竞态、逐句解冻会漏 |
| 3 | `stop media:"mibrain"` | 尝试停止原生 TTS | 3 秒补停仍有人耳漏音；解冻后存在播放动作 | 单独不通过隔离验收；不能再标为已验证主方案 |
| 4 | 客户端本地预冻结 + inotify | 抢 TTS 请求窗口 | 已实现，待真机复测 | 降低首音节漏出概率 |
| 5 | 整轮接管 + epoch/turnSeq | 修逐句解冻和旧轮次误收尾 | 已实现，待真机复测 | 必需的正确性修复 |
| 6 | wav + aplay/dmix（目标 notify） | TTS 与音乐共存 | 2026-11-02 管理台真实豆包 TTS 已通过：`notifyvol=153`、`mysoftvol=120` 不动，用户确认 TTS 播放时音乐仍在放；日志无 `mphelper pause` | 首选 notify，端到端已通过；原生唤醒/MCP 待验收 |
| 7 | mp3 + miplayer | 兼容旧格式/省流量 | 2026-11-02 实测：与音乐共存；`mysoftvol` 只影响音乐，miplayer 的 TTS 音量不变，不能独立调 TTS | 作为兼容路径，不用于可调音量的 TTS |
| 8 | LD_PRELOAD 劫持 TTS URL → 静音 | 源头阻断小爱 TTS | 未做 | 兜底实验；需要 ARM 交叉工具链 |
| 9 | LD_PRELOAD 劫持 TTS URL → 豆包 TTS | 让厂商通道播豆包 | 未做 | 复杂度高；除非 8 成功且确有需求，否则不做 |
| 10 | 网络层拦截 `speech.ai.xiaomi.com` | 阻断云端 TTS | 未做 | 预计淘汰：可能连带 ASR |
| 11 | 旧音量方案（Playback + mysoftvol） | 独立调豆包 TTS | 真机人耳复测：音乐一起变，播放器 volume 不变 | 淘汰为 TTS 音量方案；mysoftvol 固定 |
| 12 | 音量方案 B（服务端增益/豆包 volume） | 严格只控豆包 | 未实现 | 若 A 的影响面不可接受再做 |
| 13 | `player_set_volume media:"common"` | 只调音乐 | 未测 | 补充实验，确认 media 参数语义 |
| 14 | 音量方案 A（notify + notifyvol） | 独立调 TTS 通道 | 管理台真实豆包 TTS 60% 已通过（`notifyvol=153`，音乐仍在放）；用户五通道实测确认 notify 是唯一“共存 + TTS 音量独立”的通道 | 保留，tts.volume 映射到 notifyvol |

**管理台五通道实测（2026-11-02，用户人耳）**：

| 通道 | 音量旋钮 | 结果 |
|---|---|---|
| `aplay -D notify` | notifyvol | 只变说话声，音乐不变，共存（选中） |
| `aplay -D Playback` | mysoftvol | 音乐和说话声一起变，共存但音量不独立 |
| `miplayer -f` | mysoftvol | 只变音乐，TTS 音量不变，共存但 TTS 音量不可调 |
| `mediaplayer mibrain` | mediaplayer volume | 音乐停/被替换，不共存 |
| `mediaplayer common` | mediaplayer volume | 音乐停/被替换，不共存 |

**播放路径已选 notify；原生隔离方案尚未锁定**。下面组合仍是候选，已有漏音失败记录，不能作为通过验收的最终方案：

```text
冻 mico_aivs_lab + stop media:"mibrain" + 整轮接管 + 本地预冻结
+ wav/aplay -D notify 播豆包 TTS + status=0 视为中断
+ TTS 音量用 notifyvol，音乐用 mediaplayer volume，mysoftvol 固定
+ 必要时评估方案 B
```

### D7. 实验协议与验收标准

每个实验统一按下面记录：

- **准备**：音乐播放中（或指定状态），音量已知，记录 `player_get_play_status`。
- **操作**：喊「小爱同学，现在几点」或对应触发词；必要时用静音 wav/mp3 模拟 TTS。
- **观察**：
  - 是否听到小爱 TTS（人耳 + `⚠️ [native-tts]` / `🔇 [native-tts]` 日志）。
  - 音乐是否继续（`status` 是否保持 1；`🎵 [track]` 状态迁移）。
  - 首声延迟（final → 第一句 aplay 出声）。
  - 音量是否符合预期（TTS 与音乐互不影响）。
- **结论**：保留 / 淘汰 / 需要再测，并写明原因。

验收门槛：

1. 连续 5 次原生唤醒提问，0 次听到小爱 TTS。
2. 音乐在回答期间保持 `status=1`，或至少不切歌、回答结束后回到原曲目原位置。
3. 豆包第一句首声延迟不明显变差（与当前基线对比）。
4. 音量工具调整 TTS 不影响音乐，重启后恢复。
5. 所有候选方案都有记录，最终方案可追溯到实测结果。

### D8. 实验工具：管理台「音频通道测试」页

为了不每次都靠改代码/命令行对比，增加一个管理台页面（`/ui/#audiotest`，后端 `GET/POST /admin/api/debug/audio-test`）：

- 背景音乐：搜索关键词起播/停止，显示当前歌、队列和 mediaplayer 音量。
- 播放通道：`aplay -D notify`（notifyvol）、`aplay -D Playback`（mysoftvol）、`miplayer -f`（mysoftvol，故意不暂停音乐）、`mediaplayer mibrain`、`mediaplayer common`。
- 每行可独立改原始音量、输入文字并试播，返回实际命令、耗时和错误；用于人耳判断“TTS 和音乐是否共存”。
- 该页只用于实验，不参与正式对话路由；页面本身也作为实验矩阵的操作入口。

## Risks / Trade-offs

- **mips 开麦停音乐** → 用 `BeginNativeASRWindow`、`status=0` 不切歌、回合结束 `mphelper play` 原位置恢复；如果 `mphelper play` 也救不回位置，再评估音乐代理。
- **解冻后迟到的原生 TTS/播放动作** → 3 秒补停已经不能保证隔离；下一步验证按 dialog 的云端 ASR-only 或下行指令过滤，不能只拦 URL 或延长补停时间后宣称成功。
- **wav 体积大** → 局域网可接受；缓存目录需要清理策略；如果体积不可接受，再测 mp3 + miplayer 共存。
- **`mysoftvol` 会影响音乐** → 固定其值，TTS 音量不得使用它；方案 A 改用 `notifyvol`，其他 notify PCM 使用者可能一起受影响，要严格只控豆包则评估方案 B。
- **LD_PRELOAD 工具链** → 设备无编译器，Mac/3060 也没有现成 C 交叉工具链；先评估工具链，再决定是否投入。
- **冻结 aivs 影响连续唤醒/打断** → 接管期间原生唤醒无法及时处理；本地 KWS 仍可用，必要时缩短冻结窗口或改用纯 mibrain 方案。
- **`player_set_volume media` 语义未知** → 只能靠实测，不能假设。

## Migration Plan

1. 在分支 `fix/native-tts-suppress` 上完成实现与单测（已完成一部分）。
2. 部署到设备与 3060：
   - 客户端：`/data/open-xiaoai/init.sh upgrade <新二进制>`（失败自动回滚到 `client.bak`）。
   - 服务端：按 `taskkill → 覆盖 → 启动` 的既有流程，保留 `chat.exe.bak-*`。
3. 按 D7 的实验矩阵逐项测试，先测 1-7 与 14 的真实 TTS，再测 8-13。
4. 根据结果锁定最终方案；如果当前首选方案通过，合并/清理分支；如果 URL 劫持被证明必要，另开变更。
5. 回滚：客户端 `init.sh` 自带回滚；服务端恢复 `chat.exe.bak-*`；设备侧清空 `/tmp` 测试文件、恢复音量。

## Open Questions

- `mp3 + miplayer` 在去掉 `mphelper pause` 后能否与音乐共存？（D6 #7）
- 解冻后是否会出现迟到的原生 TTS？（需要真机日志）
- `player_set_volume` 的 `media:"common"` 是否真的只改音乐？（D6 #13）
- Doubao TTS 接口是否支持 `volume` 参数，还是只能在服务端做 PCM 增益？（D5 方案 B）
- ARM 交叉工具链是否可用，LD_PRELOAD 兜底是否值得投入？（D6 #8）


### D8. 实验性 SDK 指令过滤与播放器唤醒焦点

新增 `native_wake.isolation_mode: freeze|instruction`（默认 freeze）。instruction 模式由当前 client 的 12 秒租约启用，4 秒刷新；钩子在非空 final 同步认领 dialog，保留 ASR/完成事件，拦截同轮 TTS、播放、音量等动作。逐轮 claim 文件与当前 aivs PID 核验后才跳过 SIGSTOP/SIGCONT/stop；未认领轮次走已有兼容路径。最近 128 个 dialog 墓碑防止回答结束后的迟到动作，超过容量或重启不承诺永久隔离。

真实 SDK 的 31 项独立用例通过，证明 ABI/解析调用拦截路径；23:20 部署后用户确认原生 TTS 无声，但音乐仍暂停并重拉。进一步抓包与固件检查发现 mipns 内置直接 `ubus ... player_wakeup start`，绕过 wakeup.sh。单改 wakeup.sh 的对照失败且已撤回；23:31 新增临时 ubus CLI 方法过滤，只在有效租约下跳过播放器唤醒焦点的 start/stop/multistart，其他调用委托原二进制。命令重放前后 status=1，真实唤醒尚待验收。

部署、边界、备份和回滚见 `tools/native-guard/README.md`。进程级 preload 只需一次安装重启 aivs，不用每轮重启，不写全局 ld.so.preload。此方式过滤本地下行动作，**不阻止云端 ASR/NLP/TTS 请求生成**。临时挂载若未启用开机管理器，重启后消失并回退 freeze。完整标准仍是连续 5 轮人耳无小爱 TTS、音乐不中断/重头。


23:33 真实唤醒复测：用户反馈唤醒时音乐仍在，思考时消失、较久后才重新播放。服务端当时仍会在 BeginNativeTakeover 后无条件执行 mphelper play，即便焦点过滤已让音乐保持播放；随后出现重拉 URL/seek。23:36 增加“查询 status=1 则直接返回”的恢复前检查，避免重复 play/重拉/seek，已部署 chat PID 6800；新增回归用例通过。随后用户明确反馈“一直在，回答后也继续”，确认本轮思考/TTS/结束后的音乐共存；不能扩展为连续 5 轮全量验收。


复测日志：23:40:31–36、23:40:49–56、23:41:39–51 三轮均使用已认领指令隔离，客户端记录 notify/aplay 播放，未再出现 mphelper play、重拉 URL 或 seek。用户已确认其中本次询问的全程音乐继续。23:42 用户明确说“音乐停了吧”后发生停止音乐，属于明确停止指令，不应算作接管自动中断。连续 5 轮逐次人耳记录、打断、开关关闭与重启回滚验收仍保留待办；此时尚未接入开机重建。


### D9. 开机重建与失败回退

`native-guard.sh enable` 在 `/data/open-xiaoai` 保存启用标记；`init.sh` 在 AEC 准备后、client 连接前调用 boot。库、过滤器和 SHA256 清单持久保存，两个 bind mount 每次开机重建，不改只读固件。校验固件 SDK/aivs/init/ubus 与载荷后，最多等待 30 秒，允许开机更换实例，但要求当前 aivs ready/PID/maps 连续稳定 3 秒，再加载焦点过滤。已有健康覆盖时重复调用不重启进程。

校验或启动失败关闭持久开关、写失败标记，撤销租约/ready 并尝试卸载已确认属于本方案的覆盖、恢复原生 aivs；不阻止 client 启动。不匹配的其他覆盖拒绝卸载，日志报告部分回退失败。disable 同时取消持久开关和运行覆盖；故障修复后手动 enable，避免开机或客户端重启反复加载失败的库。支持范围固定为已验证 OH2P 1.62.2；固件升级必须重新验证。


2026-11-03 00:03 第二次整机重启验证通过：boot_id 已更换；aivs=2610 的 ready 与 maps 匹配；aivs 启动脚本和 ubus 两处覆盖均由开机流程重建。00:03:07 client 重连，00:03:08 服务端确认 requested=instruction / effective=instruction / enabled=true，12 秒租约恢复刷新。再次执行 boot 前后 aivs/mediaplayer/mipns/client PID 均相同，未遗留失败标记。首次真实重启触发过失败回退，兼容 client 仍能上线；修正稳定等待后成功。9 项启动状态机测试、10 项焦点过滤测试及 client Go 构建/测试与 ARM 构建通过。开机加载验收与人耳连续 5 轮验收分别记录，后者仍未完成。

2026-11-03 重启后人耳复测：用户按“先播放音乐，再询问时间”的步骤明确反馈“音乐持续，只有豆包回答”。本次通过思考、豆包 TTS 与回答结束后的音乐共存及无原生 TTS 验收，确认开机自动加载后的实际效果；仅记本轮，不计为连续 5 轮验收完成。
