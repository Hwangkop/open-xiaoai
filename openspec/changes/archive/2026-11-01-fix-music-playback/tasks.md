# Tasks

- [x] 1.1 播放工具执行后立即 `PlayPendingMusic()`。验证：真机"放首歌"→ 4 秒内 `music=true`。
- [x] 1.2 `searchAndPlay` 用搜索结果当队列并落盘。验证：点歌后 `queue=搜索：… N/15`，再说"下一首"index 递增且继续播放。
- [x] 1.3 音乐态下 `stopTTS` 跳过 `StopNativePlayback`。验证：日志出现 `🎵 音乐播放中，跳过停原生播放`，对话不再掐掉音乐。
- [x] 1.4 工具路由日志：`工具路由选择: name(args)` / `无工具调用 → action（原始输出）`；`/admin/api/debug/route` 同时返回工具路由与分类器的判断。
