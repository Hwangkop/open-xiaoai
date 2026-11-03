# 豆包网页版 TTS 的语音指令探测

日期：2026-11-03。仅独立请求合成音频，没有向音箱播放，没有修改生产音色、配置或播放链路。

## 结论

火山官方语音合成 2.0 文档明确提供 `context_texts`，但不能据此认定当前网页版 VoiceGenie 接口也支持。当前代码 `apps/chat/tts.go` 不发送该字段。本次给网页版补入字段的探测没有观察到要求的快慢语速变化，因此暂不把它作为已验证的产品能力接入。

这是“所测参数位置未验证生效”，不是证明网页版所有协议、模型和参数位置都不支持。也没有验证笑声、耳语、方言等效果。

## 官方协议与现有协议

- [能力说明](https://docs.volcengine.com/docs/doubao-voice/doubao-text-to-speech-20-capability-introduction?lang=zh)：语音指令可控制情绪、风格等；截图说明仅限 2.0 音色。
- [官方 HTTP 参数](https://docs.volcengine.com/docs/doubao-voice/unidirectional-streaming-text-to-speech-http?lang=zh)：端点为 `https://openspeech.bytedance.com/api/v3/tts/unidirectional`，`context_texts` 属于 `req_params.additions`，`additions` 是 JSON 字符串。需要官方鉴权和资源 ID。
- [音色列表](https://docs.volcengine.com/docs/doubao-voice/tone-list-3?lang=zh)：例如甜美桃子 2.0 为 `zh_female_tianmeitaozi_uranus_bigtts`。
- 当前项目调用 `frontier-audio-web-ws.doubao.com/api/v2/sami/voicegenie`，使用 `StartTask → StartSession → BidirectionalTTS → EndTTS`；当前默认音色是 `zh_female_taozi_conversation_v4_wvae_bigtts`。不能从内部名字中的 `v4` 推断它就是官方 2.0 音色。

官方请求示意，**不是当前网页协议的已验证映射**：

```json
{
  "req_params": {
    "text": "今天的天气真好。",
    "speaker": "zh_female_tianmeitaozi_uranus_bigtts",
    "additions": "{\"context_texts\":[\"请用轻松愉快的语气说话。\"]}"
  }
}
```

## 静默合成对照

每次单独创建网页 WebSocket 会话、绕过应用缓存、等待 `TTSEnded`。正文完全相同：

> 今天的天气真好，我们一起去公园散散步吧。走累了，就坐下来喝杯茶，聊聊最近发生的趣事。

慢指令：`请用非常缓慢的语速说话，每个字都慢慢说。`

快指令：`请用非常快的语速说话，像赶时间一样快速说完。`

输出均为 24kHz 单声道 PCM WAV。时长由 ffprobe 读取实际数据，不能直接使用流式 WAV 头部的占位长度。

| 请求音色 / 参数位置 | 慢指令或首次 / 秒 | 快指令或重复 / 秒 | 观察 |
|---|---:|---:|---|
| 当前音色，不加指令，重复两次 | 7.500 | 7.683 | 无参数变化也有合成波动 |
| 当前音色，`tts.extra.context_texts` | 7.509 | 7.799 | 没有符合指令方向的差异 |
| 当前音色，`tts.context_texts` | 7.808 | 7.664 | 未显示极慢/极快的明显分离 |
| 当前音色，`tts.additions` JSON 字符串 | 7.732 | 8.225 | 没有符合指令方向的差异 |
| 请求桃子 2.0 ID，`tts.extra.context_texts` | 7.419 | 7.396 | 未显示极慢/极快的明显分离 |
| 请求桃子 2.0 ID，`tts.context_texts` | 7.410 | 7.205 | 未显示极慢/极快的明显分离 |
| 请求桃子 2.0 ID，`tts.additions` JSON 字符串 | 7.581 | 7.387 | 未显示极慢/极快的明显分离 |
| 当前音色，`tts.extra` 本身改成 JSON 字符串 | — | — | 两次均收到 `SessionFailed`，无音频 |
| 当前音色，已知参数 `tts.extra.post_process.speech_rate` = 0.65 / 1.5 | 11.229 | 5.218 | 正向对照，语速参数确实能产生明显时长差异 |

另测请求桃子 2.0 ID、不加指令，时长 7.185 秒；编造不存在的音色 ID 也成功返回 7.393 秒音频。因此，仅请求成功和返回音频不足以确认服务器实际采用了指定音色。上述表格只描述“请求的 ID”，不把它当成已确认的后端模型身份。

本轮没有做人工盲听或统计显著性检验，不能仅按时长否定所有情绪控制能力；不同音频的哈希不同也不能作为指令生效的证据。用户已听到的“哈哈”来自正文与语音表现，不能据此证明 `context_texts` 支持。

探测程序和原始响应保存在本机临时目录 `/tmp/xiaoai-context-probe-20261103/`；可用 `main.go`、`round2.go` 从 `apps/chat` 目录执行 `go run <程序绝对路径> <已创建的输出目录>` 重现，`retry.go` 用于补录首轮临时目录尚未创建时未保存的三份音频。临时文件可能被系统清理。

后续若要接入：先找到网页版实际使用的字段映射，或单独验证官方 API 路径；不能直接把官方参数复制到网页请求并宣称生效。新增指令时还需把它计入 TTS 缓存键，避免不同风格复用同一音频。

## 网页源码核对与补测

同日继续打开真实豆包网页，从已加载脚本与页面公开的 chunk 清单取得当前业务代码。以下是静态调用链证据，**没有把它称为实时 WebSocket 抓帧**，也没有点击播放、改网页自动播报开关或提交聊天消息。

直接查看的源文件：

- [语音 SDK：s2-doubao-speech-sdk.b1ba3628ed.js](https://lf-flow-web-cdn.doubao.com/obj/flow-doubao/doubao/chat/static/js/async/s2-doubao-speech-sdk.b1ba3628ed.js)
- [播报业务：async-chat-speech.b01954eef4.js](https://lf-flow-web-cdn.doubao.com/obj/flow-doubao/doubao/chat/static/js/async/async-chat-speech.b01954eef4.js)
- [客户端适配：hd.8ac7fe0d04.js](https://lf-flow-web-cdn.doubao.com/obj/flow-doubao/doubao/chat/static/js/async/hd.8ac7fe0d04.js)
- [音色选择：s2-voice-selector.d4e8b25cc2.js](https://lf-flow-web-cdn.doubao.com/obj/flow-doubao/doubao/chat/static/js/async/s2-voice-selector.d4e8b25cc2.js)

业务模块 `zNK` 生成的端点仍是：

```text
wss://frontier-audio-web-ws.doubao.com/api/v2/sami/voicegenie
```

`CU` 枚举区分 `TTS=4`、`TEXT_TTS=5`、`TTS_RETRY=7`。`zNK.w` 构建对话播报会话，使用 4；`zNK.x` 构建直接文本合成会话，使用 5。前者携带会话、消息、section 等上下文标识，SDK 的 ChatTTSClient 用 `StartTTS`；后者通过 TextTTSClient 发 `BidirectionalTTS {text}` 和 `EndTTS`。两者共用端点，不能因为地址相同就认为完整行为相同。

当前仓库使用的是 `request_type=4` 加 `BidirectionalTTS`，能够合成，但与此版网页直接文本合成的类型不同。因此上面的首轮实验只覆盖了仓库原有请求方式，不能代替对类型 5 的测试。

`zNK.E` 的音色回退值为 `zh_female_wenroutaozi_uranus_bigtts`，语速/音高默认 1/0；该值是“调用者没有指定音色时”的回退，不代表已确认登录用户当前实际选中了它。音色选择模块内还留有旧 `zh_female_taozi_conversation_v4_wvae_bigtts` 的初始化状态，不能只看某一个默认值就认定全站都换了音色。

文本合成 builder 中的主要字段：

```json
{
  "business": 1,
  "request_type": 5,
  "enable_text_reading": true,
  "interrupt_type": 0,
  "query_mode": 2,
  "tts": {
    "speaker": "zh_female_wenroutaozi_uranus_bigtts",
    "audio_config": {"bit_rate": 32000, "format": "ogg_opus", "sample_rate": 24000},
    "extra": {
      "cache_config": {"text_type": 1, "use_cache": true},
      "network_level": 7,
      "post_process": {"pitch": 0, "speech_rate": 1},
      "music_ext_for_tts": ""
    }
  }
}
```

此处省略了 `chat`、会话标识及顶层 `extra`。实际 builder 还会把 post_process、cache_config、music_ext_for_tts、network_level 序列化到顶层 `extra.extra` 字符串。`music_ext_for_tts` 在所见 builder 默认值为空，不能从名称推断它支持任意语音指令。上述 SDK、业务及适配模块没有出现 `context_texts`；这不排除其他网页功能或服务端另有参数。

为补足类型差异，继续做简化的独立会话探测：改为 `request_type=5`、`query_mode=2`，正文和 WAV 格式维持首轮一致，没有复制真实用户的会话标识或凭据。这是类型 5 合成验证，不是完整重放登录态网页请求。

| 类型 5 场景 | 音频时长 / 秒 |
|---|---:|
| 仓库旧音色 ID，无指令 | 8.024 |
| 网页新回退音色 ID，无指令 | 7.408 |
| 新回退音色，`tts.extra.context_texts`，极慢 / 极快 | 7.426 / 7.391 |
| 新回退音色，顶层 `extra.extra` JSON 内的 `context_texts`，极慢 / 极快 | 7.426 / 7.401 |

六次均等到 `TTSEnded` 并保存音频，没有出现符合极慢/极快要求的明显时长分离。依然没有证明语音指令生效。未因此更换生产请求类型、音色或缓存策略。

源码快照、SHA256 对应文件和补测程序/响应/音频保存在本机 `/tmp/xiaoai-web-tts-20261103/`，可能被系统清理。业务文件 SHA256：`3431dd39259dd7e2a9c7939d9e88a435253c006a452627a52c4672e9b99a90e8`；SDK SHA256：`a36fe1e0d91cc289f274611e64bbc95b085babab2c643cecf790358d269f26b4`。
