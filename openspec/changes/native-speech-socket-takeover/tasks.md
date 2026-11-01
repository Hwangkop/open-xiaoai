# Tasks

## 1. 协议验证（只读，不影响现有链路）

- [ ] 1.1 写一个只读探针：在 `/tmp/mico_aivs_lab/usock/speech.usock` 上 bind 并注册，只打印收到的帧类型与长度，不回应任何控制
- [ ] 1.2 在设备上用 `pnshelper event_notify` 触发一次收音，确认能收到 `WAKEUP` / `ASR` 帧，记录帧头布局
- [ ] 1.3 核对 `activate_mode`、音频采样率/位深与参考实现是否一致；不一致就先补协议笔记再继续

## 2. 设备编排（init.sh）

- [ ] 2.1 增加 `speech_host` 模式：停 pns/mico、覆盖 pns 启动参数为 PCM、备份并让出 `speech.usock`
- [ ] 2.2 起宿主后用 bind mount 保护 `speech.usock`，再起 `mico_aivs_lab`（仅非语音服务）
- [ ] 2.3 增加停止/回退流程：停宿主 → 解除保护 → 还原文件 → 起 mico → 起 pns
- [ ] 2.4 增加看护：宿主退出或 socket inode 变化时重启接管，连续 3 次失败回落原生并记录

## 3. client：语音宿主

- [ ] 3.1 实现 datagram 协议编解码（REGISTER / PREPARE / TRANSMITTING / END + 下行控制）
- [ ] 3.2 唤醒后请求收音，把 PCM 送入现有 ASR 管线，VAD 判停后结束收音
- [ ] 3.3 与现有 `stop_tts` / `markSpeaking` 衔接：播报期间抑制上行，播完继续收音
- [ ] 3.4 音乐只压音量不冻播放器；接入现有 `music_duck` 配置
- [ ] 3.5 宿主生命周期上报（启动、注册、每轮语音）到服务端日志

## 4. 服务端衔接

- [ ] 4.1 新增配置 `native_wake.mode: freeze | speech_host`（默认 `freeze`），管理台登记
- [ ] 4.2 语音宿主模式的文本入口复用现有路由与主助手，不做二次唤醒词校验
- [ ] 4.3 冻结方案保留，切换模式时确保不会两套同时生效

## 5. 测试与验收

- [ ] 5.1 `go build ./... && go test ./...`、`gofmt -l`、前端 `pnpm build`
- [ ] 5.2 实机：唤醒 → 提问 → 豆包回答，全程无小爱声音
- [ ] 5.3 实机：音乐播放中问答，音乐**从原位置继续**而不是从头
- [ ] 5.4 实机：连续两轮对话；打断；静默结束
- [ ] 5.5 实机：执行停止接管，原生小爱恢复可用
- [ ] 5.6 实机：宿主被 kill 后看护能拉起；连续失败能回落

## 6. 文档

- [ ] 6.1 `docs/device-audio-aec-native-asr.md` 增加「语音宿主」一章：协议表、编排、与冻结方案的区别
- [ ] 6.2 `docs/deploy.md` 增加切换与回退步骤
- [ ] 6.3 `README.md` 更新接管章节，说明两种模式与取舍
