## Why

音乐与豆包 WAV/notify 已能共存，但同时播放时音乐会盖住回答。希望仅在豆包播报期间临时压低音乐，结束后恢复，不重新引入暂停和从头播放。

## What Changes

- 增加默认关闭的 `tts.music_duck.enabled`、保留音量比例 `percent`（默认 50），管理台可配置。
- 在首句 notify 播放前保存实际音乐音量并临时降低，整轮播报共用，结束一次性恢复。
- 隔离重叠轮次，手动音量/静音优先，保存崩溃恢复记录，串行化音乐音量操作。
- 只改音乐音量；保留现有播放路由、原生指令隔离以及顶层音乐会话降音量。

## Capabilities

### New Capabilities
- `tts-music-ducking`: 豆包播报时临时降低音乐音量，支持安全恢复。

### Modified Capabilities

无。

## Impact

服务端 Speaker、Engine、语音流水线、配置与管理台；增加配置同目录运行文件 `tts-music-duck.json`。无需修改音箱客户端或原生钩子。本地验证不播放音频；已按用户指令仅部署服务端及管理台、运行配置开启 50%，听感验收待进行。
