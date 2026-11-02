# OH2P 原生指令隔离实验

当前只验证 OH2P 1.62.2，32 位 ARM hardfloat、glibc 2.25。参考项目的 ABI 访问方式来自 [xiaomi_ai_llm](https://github.com/open-speaker-llm/xiaomi_ai_llm)，保留 MIT 许可于 `LICENSE.reference`；没有使用其他型号的现成二进制。

固件 SDK SHA256：`e480bcab16c85f8c2331463fdf54ca032bf95f48ec99af86380b0520ffdb1888`。

## 工作方式

- `native_guard.so` 只加载到 `/usr/bin/mico_aivs_lab`，在真实 JsonCpp `OurReader::parse` 返回时同步检查指令。
- 活跃 client 维持 12 秒策略租约，每 4 秒刷新。非空 final、有效 dialog、未命中 `native_keywords` 时认领本轮，保留 ASR 和完成事件，阻止同轮 TTS、播放、音量等下行动作派发。
- 每轮创建只含 aivs PID 的 `claim-<dialog>` 文件。client 核对当前 aivs PID、ready 和进程映射后，在 instruction 事件上标记 `instruction_guarded`。服务端只对已经认领的轮次跳过冻结/解冻/补停。
- 服务端 `native_wake.isolation_mode: instruction` 选择实验方式；默认 `freeze` 保留兼容路径。钩子未加载时明确报告 effective=freeze；运行中钩子失效时停止刷新租约，未认领的新轮次走冻结兼容路径。
- 断线后不再认领新轮次。已认领的最近 128 个 dialog 保留在进程内，回复结束、断线、租约过期都不放行其迟到指令；第 129 个认领开始逐项淘汰，aivs 重启清空。
- 固件 `mipns-xiaomi` 会绕过 `wakeup.sh` 直接用 ubus CLI 发送 `player_wakeup start`。`ubus-filter.sh` 只在有效租约下跳过 `mediaplayer.player_wakeup` 的 start/stop/multistart；stopalarm 与所有其他 ubus 命令原样委托备份二进制。
- 这只阻止本地下行动作执行，不阻止 ASR/NLP 请求上云，也不承诺云端不生成 TTS。豆包音频仍走 `aplay -D notify`。

## 编译和独立验证

```sh
sh tools/native-guard/build.sh
python3 tools/native-guard/test_focus_filter.py
python3 tools/native-guard/test_persistence.py
python3 tools/native-guard/make-manifest.py
```

产物默认在 `/tmp/xiaoai-native-guard-build`。将 `test_guard` 复制到对应固件的音箱后执行：独立进程使用设备 SDK，验证真实 CharReader → 预加载符号调用及 31 项用例，不开麦、不连云端、不操作现有语音进程。临时策略在 `/tmp/xiaoai-native-guard-unit`，正常结束会清理；失败后先检查具体残留再重试。

测试可证明当前 SDK 的 ABI 与指令判定行为，不能替代真实唤醒和人耳共存测试。

## 开机自动加载与回滚

推荐使用 `native-guard.sh` 统一管理；以下临时安装脚本保留用于早期实验，不要与管理器重复叠加挂载。开机仍使用 bind mount，永久保存在 `/data` 的脚本、库、校验清单和启用标记负责每次重建覆盖，不修改只读固件。

部署到 `/data/open-xiaoai/`：`native_guard.so`、`native-guard.sh`、`native-guard.sha256`、`native-guard-ubus-filter.sh` 和 `native-guard-rollback.sh`。更新 `apps/client/init.sh`，保留设备原来的 `server.txt`。校验清单由本地 `make-manifest.py` 生成，固件指纹固定为已验证版本，不能从未知设备自动采信。管理器与启动脚本需 `chmod 755`。

```sh
/data/open-xiaoai/native-guard.sh enable   # 空闲时首次安装；已有健康覆盖时不重启
/data/open-xiaoai/native-guard.sh status
/data/open-xiaoai/native-guard.sh disable  # 取消持久开关并恢复原生服务
```

启动链为 `/data/init.sh → boot.sh → init.sh → native-guard.sh boot → client`。AEC 准备完成后、client 连接前校验并加载。验证原 SDK/aivs/init/ubus 和库/过滤器的 SHA256，再检查 ready/PID/maps 与进程稳定性；最多等待 30 秒，允许实例更换，但要求当前实例连续稳定 3 秒。重复执行健康的 boot/enable 不重启 aivs、mediaplayer 或 pns。初次加载及 disable 恢复会重启 aivs，须在无音乐/无对话时操作；不能每轮执行。

失败会撤销租约和就绪标记、移除持久启用标记，写 `native-guard.failed` 并尝试卸载本方案覆盖、恢复原生 aivs；client 仍启动并使用冻结兼容路径。拒绝卸载不属于本方案的覆盖，部分回退失败会记录日志，需检查后处理。诊断见 `/tmp/xiaoai-native-guard/boot.log` 和 `/tmp/xiaoai-client.log`；修复后手动 enable，避免每次 client 重启反复加载坏库。

## 早期临时安装步骤

按 `docs/deploy.md` 连接设备并备份 client/server。不要覆盖跨平台配置。仅将 `native_guard.so`、安装/回滚脚本、`ubus-filter.sh` 复制到 `/data/open-xiaoai/`，其设备侧文件名分别为：

```text
native_guard.so
native-guard-install.sh             ← install-device.sh
native-guard-install-focus.sh       ← install-focus-device.sh
native-guard-ubus-filter.sh          ← ubus-filter.sh
native-guard-rollback.sh            ← rollback-device.sh
```

1. 音乐暂停且无对话时运行 `sh /data/open-xiaoai/native-guard-install.sh`。它临时 bind mount aivs 的启动脚本，仅向 aivs 注入 LD_PRELOAD，并一次性重启 aivs；检查 ready/PID/maps/稳定存活，失败自动撤回。**不能用重启 aivs 作为每轮收尾。** 没有写全局 `/etc/ld.so.preload`。
2. 安装新版 client/server，服务端只增改 `native_wake.isolation_mode: instruction`，保留其余平台配置。Windows 遵守 taskkill → 覆盖 → 启动。
3. 确认日志 `requested=instruction effective=instruction enabled=true` 及设备 policy 租约持续刷新。
4. 运行 `sh /data/open-xiaoai/native-guard-install-focus.sh`，临时覆盖 `/bin/ubus`，其他调用交给 `/data/open-xiaoai/ubus.native-guard.original`。安装不重启音频服务。
5. 从放歌开始验收真实唤醒：原生 TTS 不出声、音乐无暂停/重头、ASR 正常、无双执行；记录每轮日志和人耳反馈。此实验尚不能据“status=1”宣布声学验收通过。

回滚顺序：先把服务端隔离方式改回 freeze 并同步，使 client 撤销 policy；再执行 `sh /data/open-xiaoai/native-guard-rollback.sh`。该脚本撤掉租约、ubus/历史 wakeup 实验覆盖和 aivs 启动覆盖，恢复原生 aivs；如需完全回到之前部署，再恢复同平台 client/server 备份。未开启管理器时，临时挂载在整机重启后消失并回退；开启后由启动脚本重建。新回滚脚本委托管理器 disable，确保重启后不会再次加载。

## 2026-11-02 现场记录

- 独立 SDK 测试 31 项通过；client/chat Go 构建测试和前端构建通过。
- 23:20 部署指令隔离；用户确认“小爱 TTS 没有声音了”，但音乐仍停止，豆包出声前后重拉歌曲。日志证实已认领轮次拦截 Speak/SpeakStream/AudioPlayer.Play 等，并且未做回合解冻/清队列。
- 23:24–25 抓到 `player_wakeup start`，随后播放器空闲，服务端 mphelper play 失败后重拉 URL。部分恢复还发出 position，但不能据此声称实际无缝。
- 单改 wakeup.sh 没覆盖 mipns 的内置直接 ubus 调用，用户复测仍失败。该临时挂载已撤销。
- 23:31 安装 ubus CLI 的精确方法过滤；手动重放同一 start 请求，过滤日志出现且前后 status=1。真实唤醒共存仍须用户复测，不能把这个命令对照当最终验收。


23:33 真实唤醒复测：用户反馈唤醒时音乐仍在，思考时消失、较久后才重新播放。服务端当时仍会在 BeginNativeTakeover 后无条件执行 mphelper play，即便焦点过滤已让音乐保持播放；随后出现重拉 URL/seek。23:36 增加“查询 status=1 则直接返回”的恢复前检查，避免重复 play/重拉/seek，已部署 chat PID 6800；新增回归用例通过。随后用户明确反馈“一直在，回答后也继续”，确认本轮思考/TTS/结束后的音乐共存；不能扩展为连续 5 轮全量验收。


复测日志：23:40:31–36、23:40:49–56、23:41:39–51 三轮均使用已认领指令隔离，客户端记录 notify/aplay 播放，未再出现 mphelper play、重拉 URL 或 seek。用户已确认其中本次询问的全程音乐继续。23:42 用户明确说“音乐停了吧”后发生停止音乐，属于明确停止指令，不应算作接管自动中断。连续 5 轮逐次人耳记录、打断、开关关闭与重启回滚验收仍保留待办；此时尚未接入开机重建。


2026-11-02 23:57 首次真实重启：DHCP 地址变化，自动加载检测到 aivs 实例更换而回退；client 正常上线，effective=freeze，验证了失败兼容启动。随后将一次稳定检查改为 30 秒内等待当前实例连续稳定 3 秒，允许开机中途换 PID。2026-11-03 00:02 手动 enable 通过并开始第二次整机重启验证。


2026-11-03 00:03 第二次整机重启验证通过：boot_id 已更换；aivs=2610 的 ready 与 maps 匹配；aivs 启动脚本和 ubus 两处覆盖均由开机流程重建。00:03:07 client 重连，00:03:08 服务端确认 requested=instruction / effective=instruction / enabled=true，12 秒租约恢复刷新。再次执行 boot 前后 aivs/mediaplayer/mipns/client PID 均相同，未遗留失败标记。首次真实重启触发过失败回退，兼容 client 仍能上线；修正稳定等待后成功。9 项启动状态机测试、10 项焦点过滤测试及 client Go 构建/测试与 ARM 构建通过。开机加载验收与人耳连续 5 轮验收分别记录，后者仍未完成。

2026-11-03 重启后人耳复测：用户按“先播放音乐，再询问时间”的步骤明确反馈“音乐持续，只有豆包回答”。本次通过思考、豆包 TTS 与回答结束后的音乐共存及无原生 TTS 验收，确认开机自动加载后的实际效果；仅记本轮，不计为连续 5 轮验收完成。
