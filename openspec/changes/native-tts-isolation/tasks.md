# Tasks

## 1. 基线与实验台账

- [ ] 1.1 在 `docs/device-audio-aec-native-asr.md` 增加「实验矩阵」表（方案、步骤、观察指标、结论），并确认 design.md 的 14 个方案都在表里有对应行
- [x] 1.2 部署当前分支的 client 与 chat 到设备/3060，确认启动日志出现 `🛡️ 本地预冻结开关`、`🔇 [native-tts]`、`🧾 [instruction]`，验证部署可回滚
- [ ] 1.3 用 main 的冻结方案跑一次基线（音乐中喊「小爱同学，现在几点」），记录是否听到小爱 TTS、音乐是否停、首声延迟，填入实验矩阵
- [ ] 1.4 增加实验开关 `native_wake.suppress_mode`（`freeze` / `stop_mibrain` / `both` / `off`），启动时打印当前模式；验证改配置后日志与行为随之变化

## 2. TTS 屏蔽方案实测

- [ ] 2.1 测方案 #2「只冻 aivs」（`suppress_mode=freeze`）：音乐中连续 3 次原生唤醒提问，记录小爱 TTS 是否漏出、音乐 status、首声延迟，填入实验矩阵
- [ ] 2.2 测方案 #3「只清 mibrain」（`suppress_mode=stop_mibrain`）：同样 3 次，验证小爱 TTS 不出声且音乐继续，记录结果
- [ ] 2.3 测方案 #2+#3「冻 aivs + 清 mibrain」（`suppress_mode=both`）：验证组合效果，记录是否还有首音节或迟到 TTS
- [ ] 2.4 测方案 #4「本地预冻结」开/关（`native_guard` 开关）：对比首音节漏出概率与 final→冻结耗时，记录 inotify 是否把延迟压到毫秒级
- [ ] 2.5 测方案 #5「整轮接管 + epoch/turnSeq」：让豆包回复至少 3 句，确认第一句结束不解冻；再在播放中打断并开始新轮次，确认旧轮次不会结束新接管
- [ ] 2.6 验证 `Speak` 事件兜底：构造/等待接管中出现原生 `Speak`，确认日志出现 `⚠️ [native-tts] 接管中仍收到原生 Speak 事件` 且 mibrain 再次被清空
- [ ] 2.7 验证家电指令不被 TTS 隔离：音乐中说「小爱同学，开灯」，确认原生确认语音正常、预冻结被释放
- [ ] 2.8 音乐态不自动原生续听；非音乐态续听管理 ASR 窗口、匹配 dialog 终态、超时与旧窗口隔离；本轮本地回归通过，待真机确认音乐恢复后不会再次被主动开麦暂停
- [ ] 2.9 3 秒补停已有漏音失败证据：验证按 dialog 的 ASR-only/下行指令过滤可行性，覆盖原生 TTS、播放与音量动作，保留明确回滚方式；不得把缺少 stop/pause 或 stop 成功视为隔离验收

## 3. 播放路径实测

- [x] 3.1 测方案 #6「wav + aplay」：将真实豆包 WAV 播放路由从 `Playback` 改为 `aplay -D notify`，音乐中触发豆包 TTS，确认叠播、音乐 status 保持 1，记录首声延迟
- [ ] 3.2 测方案 #7「mp3 + miplayer（现有 pause 逻辑）」：`tts.format=mp3`，确认播放前 `mphelper pause`、播放后恢复，记录音乐是否从头开始
- [ ] 3.3 测方案 #7b「mp3 + miplayer 去掉 pause」：增加实验开关或临时改代码，确认 miplayer 是否能与音乐共存；记录是否可以省掉 wav
- [x] 3.4 根据 3.1-3.3 结果增加显式配置 `tts.player: aplay|miplayer`（不再靠 URL 后缀猜），并加单测验证两条分支
- [x] 3.5 验证 `tts.player` 与 `tts.format` 不一致时的错误处理：配置为 wav 但实际是 mp3，确认记录错误并回退/报错，不静默失败
- [x] 3.6 用相同真实豆包 WAV 做 `notifyvol=60/180` 对照：人耳确认 TTS 一小一大、音乐听感不变；记录 mixer 实际值和音乐状态，恢复测试前音量。短音测试通过不等于此项完成

## 4. 兜底方案可行性

- [x] 4.1 评估 ARM 交叉工具链（本机/3060/brew/zig 任一可行路径），记录能否编译出设备可用的 `.so`
- [ ] 4.2 若 4.1 可行，做方案 #8「LD_PRELOAD 把 mibrain TTS URL 改成静音 URL」实验：验证接管期间无小爱 TTS、音乐继续；记录实现文件与回滚方式
- [ ] 4.3 评估方案 #9「URL 换成豆包 TTS」：记录时序、分段、dialog 生命周期、ducking 四个阻塞点，给出不做/缓做的结论
- [ ] 4.4 做方案 #10「网络层拦截 `speech.ai.xiaomi.com`」的受控实验：确认是否连带 ASR；无论结果如何都记录，并给出淘汰/保留结论
- [ ] 4.5 汇总 4.1-4.4，更新实验矩阵的「兜底方案」行与最终选型建议

## 5. 音量方案

- [ ] 5.1 实现方案 A（#14）：`tts.volume` 配置 + `set_tts_volume` MCP 工具，底层 `amixer -c 0 sset notifyvol 0-255`；WAV 走 `aplay -D notify`，验证 TTS 音量变化、音乐听感不变，保持 `mysoftvol` 固定
- [x] 5.2 复测并淘汰旧 mysoftvol 方案（#11）：人耳确认音乐随 mysoftvol 改变，即使 mediaplayer volume 不变；notifyvol 短音对照可改变响度且音乐听感不变，结果写入 design.md 和设备文档第 13 节
- [ ] 5.3 评估方案 B：确认豆包 TTS 接口是否支持 `volume`；不支持则在服务端对 wav PCM 做增益，验证只影响豆包 TTS
- [ ] 5.4 测方案 B：TTS 50 / 音乐 80，确认互不影响；记录首声延迟与缓存 key 变化
- [ ] 5.5 测方案 C：`player_set_volume '{"volume":N,"media":"common"}'` 是否能只调音乐；记录 media 参数的真实语义
- [ ] 5.6 根据 5.2/5.4/5.5 选择最终音量方案，更新配置、MCP 工具描述与管理台字段；验证重启后音量恢复
- [ ] 5.7 验证方案 A 的影响面与 ducking：确认共享 notify PCM 的其他提示音是否受 notifyvol 影响；调整音乐 mediaplayer volume 时豆包 WAV 响度不变；不把 notifyvol 描述为豆包专属控制

## 6. 测试与文档

- [ ] 6.1 为 `suppress_mode`、`tts.player`、`tts.volume`、`trackMusic status=0`、预冻结释放/30s 兜底补单测，`go build ./... && go test ./...` 通过
- [x] 6.2 前端管理台登记新增配置字段（`ConfigView.tsx` 的 `SECTIONS`），`pnpm build` 通过
- [ ] 6.3 更新 `docs/device-audio-aec-native-asr.md`：补充 mibrain/common 通道、实验矩阵、最终选型与回滚步骤；更正第 13 节，mysoftvol 按影响音乐的全局输出增益管理，notifyvol 控制 TTS 通道，标注真实 TTS 未验收；确认文档里的命令可执行
- [ ] 6.4 更新 `AGENTS.md`/部署说明中与原生 TTS 隔离相关的约定（如有新增配置或运维步骤）

## 7. 集成验收

- [ ] 7.1 正式部署后，音乐中连续 5 次喊「小爱同学」提问：0 次听到小爱 TTS，音乐不切歌，首声延迟不明显变差；记录日志证据
- [ ] 7.2 验证打断场景：豆包回答中再次喊「小爱同学」，确认新轮次接管正常、旧轮次不误释放
- [ ] 7.3 验证回滚：客户端 `init.sh` 回滚到 `client.bak`、服务端恢复 `chat.exe.bak-*`，确认设备回到基线行为
- [ ] 7.4 把 1-14 号方案的最终结果写回实验矩阵，锁定最终方案并记录选择理由

## Workflow follow-up

- 所有实验与验收完成后，按项目流程归档本变更。
- 如果最终选择了 URL 劫持或服务端增益等新增实现，另开变更记录实现细节。


## 8. 执行前指令隔离实验（继续探索）

- [x] 8.1 针对当前 SDK 用 Zig 编译，并在音箱独立进程验证真实 CharReader 解析钩子与 31 项会话用例
- [x] 8.2 增加 instruction 隔离配置、client 短租约、逐轮 claim/PID 核验；已认领轮次不冻结、不解冻、不 stop mibrain
- [x] 8.3 完成 client/chat 测试、ARM/Windows 构建与前端构建；备份后按平台流程临时部署
- [x] 8.4 用户确认原生 TTS 无声，记录音乐仍停止/重拉的失败；抓取 player_wakeup，并确认 mipns 直接调用绕过 wakeup.sh
- [x] 8.5 实现仅过滤播放器唤醒焦点的临时 ubus wrapper，10 项边界用例通过；部署后重放 start 请求，状态保持 1
- [ ] 8.6 真实唤醒复测音乐无暂停/重头、识别正常；完成连续 5 次并保留人耳反馈（23:36 修正后用户已确认本轮“一直在，回答后也继续”；日志另外记录数轮，无重拉/seek，尚不代替逐次听感验收）
- [ ] 8.7 验证重叠轮次、断线、开关关闭、透传关键词和回滚；只在通过后锁定方案


## 9. 开机自动加载

- [x] 9.1 实现持久开关、固定固件指纹校验、幂等加载、失败关闭与持久禁用回滚；接入 AEC 准备后/client 连接前
- [x] 9.2 通过 9 项模拟挂载/进程测试，覆盖开关、重复加载、重建、实例更换后稳定、持续更换失败、固件不匹配、启动失败及其他挂载保护
- [x] 9.3 部署并接纳现有健康覆盖，重复 boot/enable 前后 aivs/mediaplayer/pns/client PID 不变
- [x] 9.4 实际重启验证挂载重建、钩子就绪、client 重连和在线租约（2026-11-03 00:03，effective=instruction）；首次失败回退亦有真机记录
- [x] 9.5 重启后人耳复测音乐全程共存和无原生 TTS（2026-11-03 用户确认“音乐持续，只有豆包回答”）；仅本轮通过，连续 5 轮仍按 8.6 记录
