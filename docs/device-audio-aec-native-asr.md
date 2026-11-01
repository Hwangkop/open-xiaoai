# 音箱音频链路与原生 ASR 逆向记录

> 适用设备：小爱音箱 OH2P，固件 1.62.2（boot1）。
> 本文记录“音乐大声时语音助手听不见”这个问题的排查、AEC 修复、固件逆向与免唤醒原生 ASR 实验。
> 命令里的 `<speaker-ip>` 请按实际环境替换；不要写入真实地址、密码或账号。

## 1. 结论速览

1. **AEC 在 mipns 进程内，不在 ALSA 层**。`Loopback Enable` 只负责把播放参考送进 AEC；`snd_pcm_readi` 读到的仍是**带满幅回声**的原始 PDM（实测音量 35 时 ch0 RMS≈8k、300Hz 单音强度≈4400）。AEC 发生在读出之后的 VPM 链路里，所以：
   - 原生唤醒/ASR 吃得到 AEC 后的音频，大音量下也能识别；
   - 我们自己的 `arecord`（另一路 dsnoop）拿不到，必须靠 ducking 压音量。
2. **参考通道必须在 PDM 采集启动前打开**，只切 mixer 不重开采集不生效。启动 client 前，`init.sh` 的 `prepare_aec_reference` 会 `amixer -c 0 sset 'Loopback Enable' Enable` 并**重启 pns** 让采集按新路由重开，再用 `/tmp/xiaoai-aec.ready` 记录已处理过的 mipns pid，避免重复重启。
3. **免唤醒原生 ASR 在 1.62.2 上可以冷启动**，不需要 LD_PRELOAD，也不需要 `activate_mode=NONWAKEUP` 改包：
   ```sh
   ubus -t 2 call pnshelper event_notify '{"src":1,"event":0}'
   ```
   实测会新建一个 `SpeechRecognizer.Recognize` 会话，`RequestState.is_init_wakeup=false`，没有 `SpeechWakeup.Wakeup` 事件；安静时 final 为空。这是后续“音乐大声时也能喊豆包”的基础。
4. **NONWAKEUP 协议本身可移植**：`libaivs-message-util.so` 里 `StreamPrepareRequestMessage.activate_mode` 在结构体偏移 `0x0c`，枚举 `WAKEUP=0 / NONWAKEUP=1`，`speech_message__pack` 可被 LD_PRELOAD 拦截。但本固件没有 `libxaudio_engine.so`，唤醒入口在 VPM/mdspeech，参考项目的 `xaudio_register_callback` 不能直接复用；既然 `pnshelper event_notify` 已能冷启动，暂时不需要走改包路线。
5. **自动续播的 `status=3` 修复已生效**（`player_get_play_status` 在自然播完后返回 3，不是 0）。

## 2. 设备音频结构

### 2.1 ALSA

```text
card 0: AMLAXGSOUND
  device 0: TDM-A-dummy
  device 1: TDM-B-dummy
  device 2: TDM-C-acm8625p（播放 codec）
  device 3: PDM-dummy（4 麦 PDM 采集，hw:0,3）
card 1: UAC2_Gadget
```

`/etc/asound.conf`：

```text
pcm.noop / pcm.Capture = plug → dsnoop(hw:0,3, 48000Hz, S32_LE, 4ch)
```

client 的 `arecord -D noop -f S32_LE -r 48000 -c 4` 读的是**原始 PDM**；取通道 0，右移 8 位（24bit → 16bit），48k→16k。

相关 mixer 控制：

```text
numid=6  'Loopback Enable'        enum: Disable / Enable
numid=7  'Loopback datain source'
numid=8  'Loopback tmdin lb source'
```

`Loopback Enable` 必须在 PDM 采集打开前设置；采集已经运行后再切，硬件不会重新初始化参考通道。

### 2.2 进程

| 进程 | 作用 |
|---|---|
| `mipns-xiaomi` | 音频前端：PDM 采集、VPM/KWS、AEC、`speech.usock` 协议 |
| `vis` | `MpdAudioSource`，读 `/tmp/mic_audio.fifo`、`/tmp/vis_audio.fifo` |
| `mico_aivs_lab` | 小爱 AIVS 会话：Wakeup / Recognize / NLP / TTS |
| `mediaplayer` | 播放器：`player_play_url`、`player_get_play_status`、`player_wakeup` |
| `arecord` | client 起的第二路 PDM 采集（dsnoop 共享） |

常用文件：

```text
/tmp/mico_aivs_lab/event.log         AIVS 事件（Wakeup/Recognize/Finish...）
/tmp/mico_aivs_lab/instruction.log   最终 ASR 文本（SpeechRecognizer.RecognizeResult）
/tmp/mico_aivs_lab/usock/speech.usock  mipns → aivs 的 protobuf 上行（音频/控制）
/tmp/mipns/usock/speech.usock          aivs → mipns 的下行
/data/mipns/audio/wakeup/<ts>/         原生唤醒音频 dump（flac 16k mono）
```

## 3. AEC 参考通道

参考项目 `open-speaker-llm/xiaomi_ai_llm` 的记录：
[2026-09-09-parallel-wake-reference.md](https://github.com/open-speaker-llm/xiaomi_ai_llm/blob/main/docs/history/2026-09-09-parallel-wake-reference.md)

关键事实：

- 固件 `xaudio_aec_thread` 输入 7 个平面 × 128 个 S16 样本：前 6 个是麦，第 7 个是播放参考。
- 参考峰值 < 10 时直接复制麦样本，跳过 AEC。
- `Loopback Enable` 默认 Disable；必须在采集启动前 Enable。
- 只在采集运行后切换无效。

本固件没有独立的 `libxaudio_engine.so`，但 VPM/KWS 的流配置里能看到对应的参考流：

```text
libflexkws.so:
  raw_stream / mic_0_16k / mic_1_16k
  mics_refs_16k / mics_refs_16k_0
  combine_mics_refs_16k
libvpm.so:
  s_vpm_wakeup_data_callback / s_vpm_asr_data_callback
  send_wakeup_data / send_wakeup_event / real_wakeup / suspect_wakeup
  stream_out_bypass_1ch / stream_out_sep_2ch
```

说明 AEC/波束成形在 VPM + flexkws 这条链路里，`Loopback Enable` 把播放参考送进 `mics_refs_16k`。

### 3.1 client 的修复

`apps/client/services/audio/mic.go`：

```go
// 在启动 arecord 之前
enableAECReference()
```

实现（`sset` 之后必须 `sget` 回读校验，控制项不存在时 `sset` 可能静默失败；调用带 3 秒超时，避免驱动卡住拖死启动）：

```go
func enableAECReference() {
    if out, err := amixerRun("-c", "0", "sset", "Loopback Enable", "Enable"); err != nil {
        log.Printf("⚠️ 开启 AEC 参考通道失败（Loopback Enable）: %v %s", err, strings.TrimSpace(out))
        return
    }
    out, err := amixerRun("-c", "0", "sget", "Loopback Enable")
    if err != nil {
        log.Printf("⚠️ 无法回读 AEC 参考通道状态: %v", err)
        return
    }
    if !strings.Contains(out, "Item0: 'Enable'") {
        log.Printf("⚠️ AEC 参考通道未生效（Loopback Enable 不是 Enable）: %s", strings.TrimSpace(out))
        return
    }
    // 这里只能确认 mixer 值；要让 AEC 真正重开采集，还得靠 init.sh 重启 pns
    log.Printf("🎚️ Loopback Enable 已设为 Enable（需 pns 重开采集才生效）")
}
```

**只设置 mixer 不够**：开机时 `pns` 在 `rc.local` 之前就已经把 PDM 采集打开了，这时再 `sset` 不会让 AEC 重新初始化。`apps/client/init.sh` 在启动 client 前做一次 `prepare_aec_reference`：

1. `sget` 读当前值；控制项不存在就直接放弃（打警告，不拦启动）；
2. 如果“已经是 Enable”且“当前 mipns 就是 marker 里记的那个 pid”，才跳过；
3. 否则不是 `Enable` 就 `sset` 并回读确认；
4. **重启 pns**，轮询等待 mipns 换 pid；
5. pid 真的变了才把新 pid 写进 `/tmp/xiaoai-aec.ready`；没变就警告且不写，下次启动重试。

### 3.2 实测（2026-11-01，只读探针）

探针做法：`LD_PRELOAD` 注入 mipns，钩 `snd_pcm_readi` 记录它读到的原始 PCM，同时包装 `vpm_init` 回调表里的 ASR 回调（`cfg+0x198`）记录送往云端的音频。播放 300Hz+1200Hz 混合单音，音量 35。**两组实验都重启过 pns 重开采集，唯一差异是 `Loopback Enable` 的值。**

| 观测点 | 参考关闭（Disable） | 参考开启（Enable） |
|---|---:|---:|
| `snd_pcm_readi` 原始采集 | （未逐通道统计） | ch0 RMS≈8k、300Hz≈4400 |
| **ASR 上行** RMS / 峰值 | **18730 / 32768（削波）** | **≈350 / ≈2500** |
| ASR 上行 300Hz 强度 | **603** | **≈10（最大 18）** |

结论：

- 两种状态下 mipns 读到的原始 PDM 都带满幅回声（关闭侧的 READ 聚合 RMS≈25k 只是读数，没做逐通道/单音统计）—— **AEC 不在 ALSA 层**。
- 参考关掉时 ASR 上行把单音原样送上去，峰值顶到 32768 削波；这排除了“上行只发人声、音乐被门控掉”的解释——上行会如实转发回声。
- 参考打开后同一段单音在 ASR 上行被压掉：按 RMS 约 35dB、按单音强度约 36dB（最差一段 31dB），按峰值只有约 22dB。所以“压制 35dB”只在 RMS/单音口径成立，**不能用峰值来支撑**。AEC 生效位置在 mipns 内部的 VPM 链路。
- 这解释了“放歌时把歌词/幻觉识别成用户说话”，也解释了原生唤醒为什么在大音量下仍然可用。
- 因此我们自己的 `arecord` 永远拿不到 AEC 后的音频；音乐态要听清命令，仍然只能靠 ducking 把音量压到 20。

参考项目 `open-speaker-llm/xiaomi_ai_llm` 的 [PR #9](https://github.com/open-speaker-llm/xiaomi_ai_llm/pull/9) 在 1.76.54 上得到同样结论（参考平面峰值 <10 时 AEC 直接跳过），并额外验证了 PNS 单独重启后需要重新设置。

## 4. 固件二进制逆向

从设备取回的文件（ARM32 hard-float，DYN，已 strip）：

| 文件 | 大小 | SHA-256 |
|---|---:|---|
| `/usr/bin/mipns-xiaomi` | 449K | `9213a9871d49bc3abc6cb0ff05d3ba9738e5951a570ead283c19ccbd2bfc724b` |
| `/usr/bin/mico_aivs_lab` | 543K | `c8cf5aa0d5d542bfe8b16b645398e1632542cbc2c2d7e3d3e110446c9a711017` |
| `/usr/lib/libaivs-message-util.so` | 37K | `9850db10a32ec659f87d125f0bdb60ae7ea8dbb6ccffe4380c4c2b0c44b9926f` |
| `/usr/lib/libvpm.so` | 110K | `f5d76a34dc40eb1b7c9045614da636fe5558344c3334a44e152d22fbfaed56d9` |
| `/usr/lib/libflexkws.so` | 3.2M | `0eeda7de83c5a2aaec91c065c30981c03ca01b5121c5382c63b80dcac6296603` |
| `/usr/lib/libmdspeech.so` | 259K | `8589357f10d699b2e9a01ac6b966cc742c17bd10685a1769f532e6be43349e2e` |
| `/usr/lib/libpns-helper.so` | 46K | `b5819b57dfe665df61eeeebfaaec8794650ae8514639853ac7c86d831af5df9a` |
| `/usr/bin/vis` | 106K | `60cea2b51b525e770fa5a411fd1b2d62d69b6ad44be6ac5d8337eacf7543813b` |
| `/usr/bin/mediaplayer` | 211K | `7e4661b99cd1d2a71817afa7fe0b4235c9727d9c3cd589c78c84fdbdf9f133ff` |

分析工具：`pyelftools + capstone + lief`（本机 Python venv 即可，不需要 Ghidra）。

### 4.1 `libaivs-message-util.so`：protobuf-c 协议

这个库用的是旧版 protobuf-c 布局：消息描述符以 magic `0x28aaeef9` 开头，枚举描述符以 `0x114315af` 开头。

`StreamPrepareRequestMessage` 描述符解析结果：

```text
sizeof_message = 28
n_fields = 3
activate_mode   id=1  type=ENUM  offset=0x0c
capture_mode    id=2  type=ENUM  offset=0x10
interact_mode   id=3  type=ENUM  offset=0x18（has bit 在 0x14）
```

`activate_mode` 枚举：

```text
WAKEUP     = 0
NONWAKEUP  = 1
```

`speech_message__upward_message__stream_prepare_request_message__init`（0x35c8）反汇编确认清零 `+0x0c / +0x10 / +0x14 / +0x18`；`speech_message__pack`（0x3b3c）是一个 4 字节跳转，直接跳到 `protobuf_c_message_pack`，可以 LD_PRELOAD 拦截。

> 参考项目在 `speech_message__pack` 里把 `activate_mode` 从 0 改成 1；这半套做法在本固件同样成立。

### 4.2 `libmdspeech.so`：MdsWakeup

导出符号：

```text
mdspeech_create / mdspeech_destroy
mdspeech_event_notify
mdspeech_data_read
mdspeech_wakeup_info_send
mdspeech_status_wakeup_set / get
```

`mdspeech_event_notify(handle, event)` 对 event 0~0x13 做 switch；event 0/1 最终调用 `0x1da80`，该函数检查 `handle+0x198` 并设置状态。它更像“唤醒状态/使能”接口，不是触发一次唤醒。

字符串里还有：

```text
/tmp/xaudio_mspeech_commu_fifo
MdsWakeup / MdspeechWakeupCallback / MdsWkpCallback
CT_WAKEUP_NOTIFY
```

### 4.3 `libvpm.so` / `libflexkws.so`

- `libvpm` 的日志字符串直接写了 `s_vpm_wakeup_data_callback`、`s_vpm_asr_data_callback`、`send_wakeup_event`、`real_wakeup`、`suspect_wakeup`。
- `libflexkws` 的 JSON 流配置包含 `mic_0_16k`、`mic_1_16k`、`mics_refs_16k`、`combine_mics_refs_16k`。
- 这两者一起构成“麦克风 + 参考 → AEC/波束成形 → KWS → 唤醒”的链路；`Loopback Enable` 决定参考流是否有内容。

本固件没有 `libxaudio_engine.so` / `xaudio_register_callback`，所以参考项目的探针不能直接拿来用；要模拟唤醒，需要在 mipns 里调用 VPM/mdspeech 的回调，或者直接走下面的 `pnshelper` 冷启动。

### 4.4 `vpm_init` 的回调表（mipns → libvpm）

`mipns-xiaomi` 调用 `vpm_init` 时把回调函数写进配置结构，偏移如下（固件 1.62.2）：

| 配置偏移 | 回调 | mipns 地址 |
|---|---|---|
| +0x188 | log callback | 初始化时传入 |
| +0x190 | event callback | `0x00040250` |
| +0x194 | wakeup data callback | `0x00040a60` |
| +0x198 | asr data callback | `0x00040f3c` |
| +0x19c | voip data callback | `0x00040aa0` |
| +0x1a8 | stat point callback | `0x00040488` |

event callback `0x40250` 的关键逻辑：

- 先校验 `arg1` 是否等于全局实例指针（`[0x947f8]`），不相等直接返回；
- `event_type == 0x300` → 重启 pns；
- `event_type == 1` 或 `6` → 写 `[全局+0x88]`，再调 `0x36860`；
- 其它类型继续走后面的分发。

这是“模拟一次唤醒/事件”的候选注入点：加载进 mipns 的 LD_PRELOAD 可以读 `[base+0x947f8]` 拿到 cookie，再调用 `0x40250(cookie, type, nil)`。

**动态实测（只读探针，2026-11-01）**：真实“小爱同学”唤醒时，VPM 依次发出：

```text
EVENT cookie=0x947f8 type=8(0x8)  param=nil
EVENT cookie=0x947f8 type=14(0xe) param=nil
EVENT cookie=0x947f8 type=0        param=nil
WAKE#1 cookie=0x947f8 data=0x...
ASR#1..N cookie=0x947f8 data=0x...
```

也就是说唤醒链路是 `8 → 14 → 0 → WAKE → ASR`。

**写探针（2026-11-01）**：在 mipns 里保存原始 event callback，收到触发文件后依次调用 `0x40250(cookie, 8/14/0, nil)`：

```text
FIRE type=8  ret=0
FIRE type=14 ret=0
FIRE type=0  ret=0
```

结果：`event.log` 出现了新的 `SpeechWakeup.Wakeup`（`wakeup_real`，`word:"小爱同学"`），但 `instruction.log` 没有 `Recognize`/`RecognizeResult`，ASR 没有开流。

结论：**事件序列能把原生系统推进到“唤醒态”**，但还缺真实唤醒里的 `WAKE` 数据回调（`0x40a60`）——它携带 score/angle/wakeup info，后续的 `Recognize` 才开始。下一步需要抓 `0x40a60` 的 `data` 结构并合成一次；只调 event callback 不够。

**抓取+回放探针（2026-11-01）**：在真实“小爱同学”唤醒时保存了 wakeup data：

```text
CAPTURE data=0x... size=48320 audio=0x...
CAPTURE ok size=48320
```

回放同一份数据后，mipns 直接进入 `D<` 状态并触发 `coredump.sh`（段错误）。说明 wakeup data 里含有不可重放的内部指针/生命周期状态，只复制结构体和音频缓冲不够。**不要重复这个回放实验**；唤醒注入需要走 NONWAKEUP prepare + IVW 门控那条路，或者接受“小爱同学”唤醒。

**IVW 门控探针（2026-11-01）**：定位到 `mipns_ivw_data_cb`（mipns `0x1a55c`），尝试零长度放行：

```text
IVW begin
IVW ret=-178257904        // 3 参数调用
IVW ret=-180355056        // 5 参数调用（1, dummy, 0, 0, 0）
```

两种调用都返回负错误码，`event.log` 没有 `Recognize`，ASR 依然不开流。

**带音频的回放（2026-11-01）**：先只读捕获真实唤醒时 wakeup data 指向的音频缓冲（`/tmp/wake.raw`，47360 字节），再用 `mipns_ivw_data_cb(1, buf, len)` 回放：

```text
offset=0    len=47360  ret=-184549360
offset=1280 len=46080  ret=-184549360
offset=0    len=46080  ret=-184549360
```

真实链路的调用是 `mipns_ivw_data_cb(len: 46080), result=1` 加 `mipns_wakeup_audio_cache(len: 46080)`，而事后回放一律返回 `-184549360`，仍然没有 `Recognize`。说明 `mipns_ivw_data_cb` 依赖 KWS 线程内部的实时状态和唤醒缓存，不能跨线程/事后喂音频，“抓一份真数据重放”这条路走不通；要做到 NONWAKEUP，得直接接管 KWS 线程或替换整个唤醒状态机，成本和风险都高。

**结论**：NONWAKEUP 的协议字段（`activate_mode=0x0c`）和事件回调已经确认，但“冷启动开麦”的 IVW 门控在本固件没有打通。继续做 NONWAKEUP 需要重写整个唤醒状态机，成本和风险都高。

不过 3.2 的实测把问题拆开了：**难的是“在音乐里听见唤醒词”，而这件事原生的 KWS + AEC 已经免费做完了**。所以更划算的路线是“借唤醒”：

1. 原生 `mipns` 用 AEC 后的音频跑 KWS，命中「小爱同学」；
2. 我们只取这个唤醒事件（hook `vpm_init` 回调表 `cfg+0x194`，或监听 syslog）；
3. 拿到事件后立刻把音乐压到 20，后面的话用我们自己的 ASR 收；
4. 同时停掉 `mico_aivs_lab`（或在 `Execution.RequestControl` 里关掉 NLP/TTS），让原生助手没有机会回答。

这样唤醒词仍是「小爱同学」，但后续对话完全由我们接管。代价是 `mico_aivs_lab` 停掉后，小爱原生的天气/闹钟/米家技能也一起消失。

### 4.5 `mediaplayer` 的 `wakeup` / `is_wakeup`

`mediaplayer` 导出了 `wakeup`（0x1effc）和 `is_wakeup`（0x19488），但它们是播放器层的唤醒/播放状态，不是 ASR 会话入口。

## 5. 免唤醒原生 ASR 冷启动（重点）

### 5.1 发现

在设备空闲时执行：

```sh
ubus -t 2 call pnshelper event_notify '{"src":1,"event":0}'
```

`/tmp/mico_aivs_lab/event.log` 新增一条：

```json
{"header":{"name":"Recognize","namespace":"SpeechRecognizer"},
 "payload":{"asr":{"lang":"","vad":true},"enable_natural_record_v2":true,...}}
```

同时 `RequestState.is_init_wakeup=false`，**没有** `SpeechWakeup.Wakeup` / `WakeupStreamFinished`。安静 2 秒后 `instruction.log` 出现空的 final：

```json
{"header":{"name":"RecognizeResult","namespace":"SpeechRecognizer"},
 "payload":{"is_final":true,"results":[{"confidence":0.0,"origin_text":"","text":""}]}}
```

这说明：**没有“小爱同学”也能开麦，由原生 VAD 控制收音和结束**。`src=1,event=0` 正是 client 的 `usock` 代码里 `requestFollowupCapture` 使用的参数（`apps/client/services/audio/usock.go`）。

### 5.2 与参考项目的差异

- 参考项目 `followup-exploration.md` 试过 `pnshelper event_notify` 的 `src=3,event=4/6` 等组合，结论是“开不了麦”；本固件 `src=1,event=0` 可以，说明不同固件的 pnshelper 事件映射不同。
- 参考项目最终走的是 `activate_mode=NONWAKEUP` + `speech_message__pack` 改包；我们这台已经有更简单的冷启动入口。

### 5.3 尚未完成

冷启动会话仍然是一个**普通原生 dialog**：如果识别到非空文本，`mico_aivs_lab` 会继续走原生 NLP/TTS。要把它接到豆包，需要：

1. **抑制原生 NLP/TTS**：
   - 参考项目的做法是在 `Execution.RequestControl` 里设置 `disabled=["NLP","TTS"]`（ASR-only）。
   - 或者在本服务收到 final 后立刻 `stop_native` / `StopNativePlayback`，但原生动作可能已经开始执行，需要实测。
2. **连续收音**：
   - 原生 VAD 空闲约 6 秒后结束；要持续监听，需要在 final/结束时再次 `pnshelper event_notify`（或使用 `ContinueDialog`）。
3. **路由**：
   - 把 `instruction.log` 的 final 文本接到现有 `handleInstruction`：识别“豆包豆包”/“豆包”、音乐指令、普通问题，并避免和 PC ASR 重复触发。
4. **真人高音量验收**：
   - 音量 40、音乐播放中，冷启动后说“豆包豆包，下一首”，确认 final 文本和动作。

## 6. 备选方案：`speech.usock` 接管

client 已经实现 `-capture-backend usock`（`apps/client/services/audio/usock.go`）：

- 停 pns / mico_aivs_lab
- 把 pns 启动参数里的 `-r opus32` 去掉，改成 PCM
- 自己 bind `/tmp/mico_aivs_lab/usock/speech.usock`，注册成语音服务
- 收到 `upStreamTransmitting` / `streamASR` 的 PCM（AEC 后）
- 支持 `downExpectSpeech`、`downDialogFinish`、`pnshelper event_notify` 续录
- 退出时恢复原生服务，带 watchdog

这条路线的好处是不依赖原生 ASR/NLP，直接拿干净 PCM 给 SenseVoice；代价是要接管原生语音栈，风险更高。当前 AEC 修复已经能让 PC ASR 在 40 音量下工作，所以 usock 只作为备选。

## 7. 参考项目

- [open-speaker-llm/xiaomi_ai_llm](https://github.com/open-speaker-llm/xiaomi_ai_llm)
  - `docs/history/2026-09-09-parallel-wake-reference.md`：AEC 参考通道、Loopback 初始化时机
  - `docs/history/2026-09-06-boot1-native-asr-research.md`：NONWAKEUP 入口
  - `docs/history/2026-09-06-boot1-native-followup.md`、`docs/history/first-turn-endpoint/`：原生 ASR-only、连续追问、判停
  - `device/native_asr/`：ASR-only 实现、`activate_mode` 改包、提示音隔离
  - 适用 S12A / MDZ-25-DA / ROM 1.76.54；与本机 1.62.2 的 ABI、库和事件映射不同，不能直接复用二进制。
- [morluto/rea](https://github.com/morluto/rea)：通用逆向工程 MCP（Ghidra/Hopper/IDA/JADX/JS/.NET），不是音频库。仅在需要进一步逆向 `libvpm`/`libflexkws` 的 VPM 回调、做动态探针时作为工具使用。

## 8. 复现命令

```sh
# 1. 看参考通道状态
ssh root@<speaker-ip> "amixer -c 0 sget 'Loopback Enable'"

# 2. 重启 client —— init.sh 会自动 Enable + 重启 pns 重开采集 + 写 marker
ssh root@<speaker-ip> "/data/open-xiaoai/init.sh stop; /data/open-xiaoai/init.sh daemon"
ssh root@<speaker-ip> "cat /tmp/xiaoai-aec.ready; pidof mipns-xiaomi"   # 两者应相等

# 3. 冷启动原生 ASR（无唤醒词）
ssh root@<speaker-ip> "ubus -t 2 call pnshelper event_notify '{\"src\":1,\"event\":0}'"

# 4. 看事件与最终文本
ssh root@<speaker-ip> "tail -20 /tmp/mico_aivs_lab/event.log; tail -10 /tmp/mico_aivs_lab/instruction.log"
```

要验证“AEC 是否真的生效”，用 3.2 的只读探针：`LD_PRELOAD` 一个钩了 `snd_pcm_readi` 和 `vpm_init` ASR 回调的 so，放一段单音后对比两路的 RMS —— 原始采集会一直是满幅，ASR 上行按 RMS 应该低 30dB 以上（峰值不要拿来做判据）。探针只读不写，跑完解除 bind mount 并重启 pns 即可。

服务端日志里，小爱原生 ASR 的最终文本是：

```text
🗣️ 用户: <text>
```

PC ASR 是：

```text
⏱️ [识别] 唤醒 wake_provider=local engine=sensevoice ... text="..."
```

## 9. 风险与边界

- `pnshelper event_notify` 会真正打开原生麦克风并创建 dialog；非空文本会进入原生 NLP/TTS。未做抑制前不要在正式环境高频调用。
- `Loopback Enable` 只是接通 AEC 参考，不会改变播放内容；但在其他型号/固件上控制名可能不同，先 `amixer -c 0 controls | grep -i loopback` 确认。
- 不要修改固件分区；本文所有操作都是运行时的（`/tmp`、ALSA 控制、client 参数）。
- 重新刷机或换固件后，库哈希、事件映射、`activate_mode` 偏移都可能变化，需要重新核对。

## 10. 参考项目：duhow/xiaoai-patch

仓库：<https://github.com/duhow/xiaoai-patch>

### 10.1 项目定位

- 目标是**重做整份 rootfs**：去掉小米专有语音栈，换成 MPD / Snapcast / Shairport-Sync / Upmpdcli / Squeezelite + Porcupine + Whisper + Home Assistant。
- 支持机型：LX06 / L06A（小爱音箱 Pro）、LX01 mini、L09A Art 等；**不支持 OH2P**（本项目设备），机型表里没有它。
- 构建方式：Docker 里交叉编译一批软件包，然后 `unsquashfs` 原 rootfs、打补丁、`mksquashfs`，刷到备用分区。
- `scripts/24_remove_property_bin.sh` 会删除 `mipns-xiaomi`、`mico_aivs_lab`、`libvpm.so`、`libxaudio_engine.so` 等专有组件；这和本项目“保留小爱 + 接豆包”的方向相反。

### 10.2 它的语音助手怎么做

`packages/porcupine/config/launcher`：

```sh
MIC="mico_record"
RECORDING_CHANNEL=1
# LX06 特殊处理
if [ "${MODEL}" = "LX06" ]; then
  amixer sset 'pdm dclk' 'pdm dclk 3.072m, support 8k/16k/32k/48k'
  amixer sset 'HCIC shift gain from coeff' 'shift with -0x4'
  RECORDING_CHANNEL=8
fi
```

流程是 Porcupine 唤醒 → `arecord -D mico_record` 录音 → HA STT/Whisper → HA Conversation → TTS。STT 期间把音量压到 `VOLUME_DURING_STT=10`，**没有使用 AEC**。
这和我们现在的 ducking 思路一致，但它没有解决“高音量下识别”的问题，只是把音量压得很低。

### 10.3 仓库里的 AEC 工具（重点）

`packages/ec` 来自 <https://github.com/voice-engine/ec>：

- `ec`：没有硬件 loopback 的设备用，靠 FIFO/PulseAudio 把播放数据喂进来。
- `ec_hw`：有硬件 loopback 的设备用，**把播放参考作为输入的一个通道**。
- 用法示例：
  ```sh
  ec_hw -i plughw:1 -c 8 -l 7 -m 0,1,2,3
  ```
  含义：输入 8 通道，**通道 7 是播放参考**，0/1/2/3 是麦克风；AEC 后写 `/tmp/ec.output`。
- 实现：SpeexDSP，`speex_echo_state_init_mc(frame_size, filter_length, out_channels, ref_channels)`，10ms 帧，`speex_echo_cancellation(near, far, out)`。
- 这和 `xiaomi_ai_llm` 的“7 个平面，第 7 个是参考”是同一套模型；LX06 的 8 通道采集正好对应。

`packages/aec-cmdline` 来自 <https://github.com/dr-ni/aec_cmdline>：

- 输入是一路**立体声 8k S16_LE**：左声道麦克风、右声道播放参考（由 `snd-aloop` 虚拟设备合成）。
- 输出单声道 AEC 后音频；依赖 `snd-aloop` 和 ALSA 虚拟设备配置，适合 PC/Raspberry Pi。

### 10.4 对 OH2P 的适用性

- OH2P 不在支持列表；rootfs 补丁、模型脚本、8ch/2ch ALSA 配置都不能直接套。
- 但 `ec_hw` / `aec_cmdline` 是通用 ARM32 工具，可以单独交叉编译。
- 关键前提是 `hw:0,3` 是否暴露 8 通道且第 7 通道真的是播放参考：
  - 本机当前 `/etc/asound.conf` 的 `pcm.Capture` 是 **4ch**；
  - 原生 `mipns` 和 client 的 dsnoop 已经占用 `hw:0,3`，直接用不同 `ipc_key` 再开 8ch 会 `Device or resource busy`；
  - 要验证必须在受控窗口里停掉 pns / mico_aivs_lab / client，先开 8ch 抓 2 秒，再恢复。这个实验会短暂中断语音栈，不要在正式使用时做。
- 如果 8ch 可用，可以把它作为**用户态 AEC**的备选：`ec_hw -c 8 -l 7 -m 0,1,2,3` 输出 AEC 后 PCM，再喂给 SenseVoice；但当前硬件/VPM AEC + `Loopback Enable` 已经能工作，优先级不高。

### 10.5 结论

`duhow/xiaoai-patch` 不是给 OH2P 用的刷机包，但它是目前最完整的“小爱音箱替换语音栈”工程，提供了两个可直接借鉴的点：

1. **AEC 参考通道就是第 7 通道**（8ch 输入时），与 `xiaomi_ai_llm` 的 7 平面模型互相印证；
2. **`ec_hw` 是一个现成的 SpeexDSP 用户态 AEC 工具**，如果以后硬件/VPM AEC 不够，可以作为设备端或服务端的备选。

## 11. 原生唤醒接管（已实现）

目标：还是喊「小爱同学」，但这一轮对话由豆包回答。实现见 `apps/chat/speaker_native_takeover.go` 与 `apps/client/utils/nativeproc*.go`。

### 11.1 链路

```text
小爱同学（原生 KWS + AEC）
  → 原生云 ASR 出 final（instruction.log）
  → 服务端 tryNativeTakeover：冻结 mediaplayer（SIGSTOP）
  → 文本直接进主助手（跳过唤醒词二次校验）
  → 回答走 miplayer 播报（与 mediaplayer 相互独立）
  → 播完解冻、清掉原生播放队列、恢复音乐
  → 再调 pnshelper event_notify 重开一次原生收音，支持连续对话（最多 6 轮）
```

### 11.2 实测结论

- 在非空 final 到达时 `SIGSTOP mediaplayer`，1 秒后原生 `Speak` 事件照样出现，但**完全听不到** —— 原生 TTS 确实被冻住。
- 冻结期间 `miplayer -f xxx.mp3` **正常出声** —— 两条播放路径互不影响。
- 唤醒应答「我在」由 mips 直接写 ALSA，不经过 `mediaplayer`，冻结挡不住。
- `mediaplayer` 偶尔会被外部 `SIGCONT`（实验里观察到一次），所以冻结期间每 300ms 复核、必要时补冻；连续两次失败就放弃接管。
- 冻结期间原生回答已经排进播放器，**先解冻再 stop 会晚一拍**（能听到小爱冒一声）。改成结束接管时直接 `kill -9` 播放器、让 procd 拉起干净实例，放歌时再重新起播当前曲目。

### 11.3 与本地唤醒的分界

- 句子里含「小爱同学」→ 本地唤醒判定直接跳过，留给原生链路。
- 只说「小爱」/「豆包豆豆」→ 本地链路。
- 两路 ASR 在 2.5 秒内产出相同文本时只处理一次，避免问两遍。
- 接管轮次结束后不续开本地对话窗口，避免把小爱播报或环境声当成指令。

### 11.4 配置

```yaml
native_wake:
  takeover_enabled: true   # 默认关，验收后再打开
  max_seconds: 25          # 单轮上限，超时强制解冻
```

## 12. 语音宿主实验（分支 `feature/speech-host`，未合并）

目标：停掉 `mico_aivs_lab`，由 client 自己 bind `speech.usock`，原生助手完全不参与，音乐不再被冻结。

### 12.1 已打通

- 协议是 unix datagram + protobuf：上行 `REGISTER / STREAM_PREPARE / STREAM_TRANSMITTING / STREAM_END`，下行 `REGISTER_RESPONSE / PREPARE_RESPONSE / STOP_CAPTURE / DIALOG_FINISH`。
- PNS 去掉 `-r opus32` 后注册的 codec 为空（PCM），ASR 帧 1920 字节（16k mono 60ms）。
- 实机跑通：喊「小爱同学」→ 宿主收到 `PREPARE activate=0` → PCM 上行 → 本地 ASR → 豆包回答，全程没有「我在」、没有小爱插话。

### 12.2 卡住的地方

PCM 模式下 **`mipns-xiaomi` 会长期持有 `/dev/snd/pcmC0D2p`**（它要播自己的唤醒提示音），`mediaplayer` 拿不到播放设备：

```text
$ 遍历 /proc/*/fd 找 pcmC0D2p
1951 mipns-xiaomi
```

表现就是"假播放"：`player_get_play_status` 返回 `status=1`，但进程一个 `/dev/snd` 句柄都没有，音乐无声；恢复时只能重拉 URL，于是从头播放。

另一个代价：宿主模式下麦克风只有 mips 在开，而它只认「小爱同学」。client 同时会关掉 `arecord`（`-mic-stream` 与 `-speech-host` 互斥），所以**「豆包豆豆」这类本地唤醒词会完全失效**，只剩小爱同学一个入口。

### 12.3 试过但无效

- `dmix`：`/etc/asound.conf` 里的 `dmixer` 抢不到 `hw:0,2`（`Unknown slave PCM hw:0,2`），说明 mediaplayer 是独占打开的。
- `mphelper pause/play`：`pause` 后状态变 0（不是 2 暂停），`play` 能回到 1，但音乐仍从头开始。
- `player_set_positon`：设备对 URL 流不回报 `play_song_detail.position`（恒 0），无法验证也无法依赖。
- `player_play_url` 的 `type` 参数：`type=2/0/3` 不会把音乐顶掉，但也没有证明 TTS 真的盖在音乐上播。

### 12.4 下一步（未做）

1. **抓厂商自己的播放调用**（优先级最高）：LD_PRELOAD 钩 `mico_aivs_lab` 的 `ubus_invoke`/`system`，让原生小爱在放歌时说一句话，记录它到底调了哪个方法、带了什么参数。用户观察过"小爱说话时音乐只是变小声"，说明厂商有一条叠加播放 + 压音量的路径，抄它比自己猜参数靠谱。
2. `player_play_music` 的 `startOffset` 是否能让重拉从中断处开始。
3. 自建音乐代理：音频经我们转发，按字节偏移续播（改动大，但确定可行）。

### 12.5 结论

语音宿主与 `mediaplayer` 的音乐播放存在设备级互斥，暂不启用；默认仍是第 11 节的冻结方案。代码在 `feature/speech-host` 分支，OpenSpec 提案为 `native-speech-socket-takeover`。

## 13. 音量分层（实测）

这台设备的音量不是一个值，而是三层叠出来的：

| 层 | 控制方式 | 影响谁 | 实测 |
|---|---|---|---|
| `Master` | `amixer -c 0 sset Master` | 全部 | 16 / 100% |
| `mysoftvol` | `amixer -c 0 sset mysoftvol`（0-255） | 走 `pcm.Playback` 的客户端 | 80 → 31%，255 → 100% |
| `mediaplayer` 自己的 volume | `ubus ... player_set_volume`（0-100） | 音乐、原生 TTS | ducking 压到 20 |

关键结论：

- 我们的 TTS 走 `miplayer`，会经过 `mysoftvol`。所以 **TTS 的响度上限是 `mysoftvol`**，跟播放器那个 20 无关。
- `mysoftvol` 停在 80（31%）时，TTS 听起来明显偏小；拉到 255（100%）后立刻正常。
- ducking 压的是播放器自己的 volume（20），音乐变小、TTS 不变——这也是 ducking 能工作的原因。

要让 TTS 和音乐各自独立控制，得让 TTS 绕开 `mysoftvol`（换 PCM 设备）或者播放前临时抬高、播完还原。目前的做法是把 `mysoftvol` 拉满，音乐再靠播放器自己的音量压住。
