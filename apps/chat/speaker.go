package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
)

// playState 语音会话的全部可变状态。
//
// 这些字段之间有隐含的不变量（比如"静音时 applied 一定不等于当前音量"、
// "duckOriginal >= 0 才处于降音量状态"），所以必须由同一把锁保护、
// 通过 Speaker 的方法整体转移，禁止外部直接读写。
type playState struct {
	// 播报
	speaking     bool
	speakingCool time.Time // 播完后压一小段尾巴，避免 TTS 尾音被当人声

	// 音乐
	music         bool
	musicStart    time.Time
	musicURL      string // 当前曲目地址，播报打断后靠它重新拉起
	musicTracking bool
	pendingURL    string
	immediatePlay bool        // 路由等"没有 AI 回复"的场景：点歌要立刻起播
	musicEnded    func() bool // 一首歌自然播完时调用：返回 true 表示已接上下一首

	// 降音量（ducking）
	duckEnabled  bool
	duckTarget   int
	duckOriginal int // -1 = 不在降音量状态
	duckApplied  int
	duckBusy     bool
	mutedPrev    int // -1 = 没静音

	// 指示灯
	ledOn       bool
	ledAt       time.Time
	ledDeadline time.Time

	thinkingSilence bool
}

type Speaker struct {
	Status string // "playing", "paused", "idle"

	stMu sync.Mutex
	st   playState

	ttsMu      sync.RWMutex
	doubaoTTS  *DoubaoTTS
	ttsBaseURL func() string

	// 音量读写；测试里替换成假实现，nil 时走设备 shell。
	volumeGet func() (int, error)
	volumeSet func(int) error
}

var stopTTSTimeoutMs uint64 = 1500

func NewSpeaker() *Speaker {
	s := &Speaker{Status: "idle"}
	s.st.duckEnabled = true
	s.st.duckTarget = defaultMusicDuckVolume
	s.st.duckOriginal = -1
	s.st.duckApplied = -1
	s.st.mutedPrev = -1
	return s
}

// SetDoubaoTTS 切换语音合成实现；tts 为 nil 表示回退到音箱自带 TTS。
// baseURL 返回音箱可访问的本服务地址（如 http://192.168.1.10:4399），用于拼音频下载链接。
func (s *Speaker) SetDoubaoTTS(tts *DoubaoTTS, baseURL func() string) {
	s.ttsMu.Lock()
	defer s.ttsMu.Unlock()
	s.doubaoTTS = tts
	s.ttsBaseURL = baseURL
}

func (s *Speaker) AbortXiaoAI() error {
	log.Println("🔇 打断小爱...")
	_, err := s.runShell("/etc/init.d/mico_aivs_lab restart >/dev/null 2>&1", 10000)
	return err
}

// StopTTS 轻打断：终止 client 端当前 tts_play.sh / miplayer 进程
// Speaking 报告当前是否在播报（含结束后 700ms 冷却，避免 TTS 尾音被当成人声）。
func (s *Speaker) Speaking() bool {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	if s.st.speaking {
		return true
	}
	return time.Now().Before(s.st.speakingCool)
}

func (s *Speaker) markSpeaking(on bool) {
	s.stMu.Lock()
	s.st.speaking = on
	musicOn := s.st.music
	if !on {
		s.st.speakingCool = time.Now().Add(1500 * time.Millisecond)
	}
	s.stMu.Unlock()

	// 播报会抢占音频设备，musicplayer 被顶成暂停；说完把音乐接回去。
	// 只在"播报前就在放音乐"时做，避免把用户已经停掉的歌重新放出来。
	if !on && musicOn {
		go s.resumeMusicAfterSpeech()
	}
}

// resumeMusicAfterSpeech 播报结束后恢复音乐播放。
// miplayer 播完会把 mediaplayer 顶成空闲（不是暂停），所以先试 resume，
// 设备真的空了就把当前曲目重新拉开（从头发，设备不报位置，拿不到断点）。
func (s *Speaker) resumeMusicAfterSpeech() {
	time.Sleep(400 * time.Millisecond) // 等 miplayer 释放音频设备

	s.stMu.Lock()
	url := s.st.musicURL
	s.stMu.Unlock()
	if url == "" {
		return
	}

	if res, err := s.runShell("ubus -t 2 call mediaplayer player_get_play_status", 3000); err == nil && res != nil {
		switch mediaPlayerStatus(res.Stdout) {
		case 1:
			return // 还在放，什么都不用做
		case 2:
			if _, err := s.runShell(`ubus -t 3 call mediaplayer player_play_operation '{"action":"play","media":"common"}'`, 4000); err == nil {
				log.Printf("🎵 播报结束，音乐继续")
				return
			}
		}
	}

	if err := s.PlayURL(url, false); err != nil {
		log.Printf("⚠️ 恢复音乐播放失败: %v", err)
		return
	}
	s.stMu.Lock()
	s.st.musicStart = time.Now()
	s.stMu.Unlock()
	log.Printf("🎵 播报结束，重新拉起当前曲目")
}

func (s *Speaker) StopTTS() error {
	log.Println("⏹️ 轻打断: 终止当前 TTS")
	_, err := connect.GetRPC().CallRemote("stop_tts", nil, &stopTTSTimeoutMs)
	return err
}

// ReleaseNativeSuppression 解除 client 端对原生播放的压制。
// 必须在播我们自己的音频之前调用，否则压制窗口会把 miplayer 一起停掉。
func (s *Speaker) ReleaseNativeSuppression() {
	if _, err := connect.GetRPC().CallRemote("stop_native", map[string]int{"suppress_ms": 0}, &stopTTSTimeoutMs); err != nil {
		log.Printf("⚠️ 解除原生压制失败: %v", err)
	}
}

// ledTTL 灯亮着但一直没有新活动时，自动熄灭的时限。
// 说话、识别、思考、播报都会刷新，所以正常对话中不会闪断；纯噪声触发时不会一直亮着。
const ledTTL = 25 * time.Second

// ShowThinkingLED 点亮"正在听/正在想"的灯效（编号 15：彩色渐变）。
// 每次调用都会续期：说话、ASR、AI 思考、播报期间都保持亮着。
func (s *Speaker) ShowThinkingLED() { s.showLED(ledTTL) }

// showLED 点亮并续期，ttl 到期后自动熄灭。
func (s *Speaker) showLED(ttl time.Duration) {
	s.stMu.Lock()
	s.st.ledDeadline = time.Now().Add(ttl)
	if s.st.ledOn {
		s.stMu.Unlock()
		return
	}
	s.st.ledOn = true
	s.st.ledAt = time.Now()
	s.stMu.Unlock()

	log.Printf("💡 指示灯亮（在听/在想）")
	go func() {
		if _, err := s.runShell("/bin/show_led 15", 4000); err != nil {
			s.stMu.Lock()
			s.st.ledOn = false
			s.stMu.Unlock()
			log.Printf("⚠️ 点亮思考灯失败: %v", err)
		}
	}()
	go s.ledWatchdog()
}

// ledWatchdog 超过 ledTTL 没有新活动就自动熄灯（防止噪声误触发后一直亮着）。
func (s *Speaker) ledWatchdog() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		s.stMu.Lock()
		on, deadline := s.st.ledOn, s.st.ledDeadline
		s.stMu.Unlock()
		if !on {
			return
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			s.HideThinkingLED()
			return
		}
	}
}

// HideThinkingLED 熄掉思考灯；没点亮就什么都不做。
func (s *Speaker) HideThinkingLED() {
	s.stMu.Lock()
	s.st.ledDeadline = time.Time{}
	if !s.st.ledOn {
		s.stMu.Unlock()
		return
	}
	s.st.ledOn = false
	litAt := s.st.ledAt
	s.stMu.Unlock()

	log.Printf("💡 指示灯灭")
	// 至少亮 1.2 秒，避免短回复时灯只闪一下
	elapsed := time.Since(litAt).Milliseconds()
	go func() {
		if wait := int64(1200) - elapsed; wait > 0 {
			time.Sleep(time.Duration(wait) * time.Millisecond)
		}
		_, _ = s.runShell("/bin/shut_led 15", 4000)
	}()
}

// StartThinkingSilence 播一段无声 WAV，让音箱的播放灯亮起来（思考态）。
// 放音乐时灯本来就亮，不再叠加，避免把音乐顶掉。
func (s *Speaker) StartThinkingSilence() {
	if s.MusicPlaying() {
		return
	}
	s.ttsMu.RLock()
	fn := s.ttsBaseURL
	s.ttsMu.RUnlock()
	if fn == nil {
		return
	}
	base := strings.TrimRight(fn(), "/")
	if base == "" {
		return
	}
	s.stMu.Lock()
	if s.st.thinkingSilence {
		s.stMu.Unlock()
		return
	}
	s.st.thinkingSilence = true
	s.stMu.Unlock()
	go func() {
		script := fmt.Sprintf(`ubus -t 5 call mediaplayer player_play_url '{"url":"%s/silence.wav","type":1}'`, base)
		if _, err := s.runShell(script, 8000); err != nil {
			s.stMu.Lock()
			s.st.thinkingSilence = false
			s.stMu.Unlock()
			log.Printf("⚠️ 思考静音播放失败: %v", err)
		}
	}()
}

// StopThinkingSilence 停掉思考静音；没有在播就什么都不做。
func (s *Speaker) StopThinkingSilence() {
	s.stMu.Lock()
	was := s.st.thinkingSilence
	s.st.thinkingSilence = false
	s.stMu.Unlock()
	if !was {
		return
	}
	_, _ = s.runShell(`ubus -t 3 call mediaplayer player_play_operation '{"action":"stop"}'`, 4000)
}

func (s *Speaker) StopNativePlayback() error {
	log.Println("⏹️ 停掉小爱原生播放")
	_, err := connect.GetRPC().CallRemote("stop_native", nil, &stopTTSTimeoutMs)
	return err
}

func shellEscapeSingle(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

func (s *Speaker) PlayTTS(text string, blocking bool) error {
	s.markSpeaking(true)
	defer s.markSpeaking(false)
	if s.doubaoTTSInstance() != nil {
		return s.playDoubaoTTS(text, blocking)
	}
	if blocking {
		return s.PlayTTSWithTimeout(text, 10*60*1000)
	}
	go func() {
		if err := s.PlayTTSWithTimeout(text, 15000); err != nil {
			log.Printf("⚠️ TTS 播放失败: %v", err)
		}
	}()
	return nil
}

// PlayTTSVoice 带情绪播报（空 = 配置默认）。
func (s *Speaker) PlayTTSVoice(text string, blocking bool, emotion string) error {
	s.markSpeaking(true)
	defer s.markSpeaking(false)
	if s.doubaoTTSInstance() != nil {
		return s.playDoubaoTTSVoice(text, blocking, emotion)
	}
	if blocking {
		return s.PlayTTSWithTimeout(text, 10*60*1000)
	}
	go func() {
		if err := s.PlayTTSWithTimeout(text, 15000); err != nil {
			log.Printf("⚠️ TTS 播放失败: %v", err)
		}
	}()
	return nil
}

func (s *Speaker) PlayTTSWithTimeout(text string, timeoutMs uint64) error {
	// 配了豆包语音就优先用豆包，否则回退音箱自带 tts_play.sh。
	// （管理台"试播"和引擎回复必须走同一条路，否则听到的音色不一致。）
	if s.doubaoTTSInstance() != nil {
		return s.playDoubaoTTS(text, true)
	}
	script := fmt.Sprintf("/usr/sbin/tts_play.sh '%s'", shellEscapeSingle(text))
	_, err := s.runShell(script, timeoutMs)
	return err
}

func (s *Speaker) doubaoTTSInstance() *DoubaoTTS {
	s.ttsMu.RLock()
	defer s.ttsMu.RUnlock()
	return s.doubaoTTS
}

// playDoubaoTTS 先用豆包语音合成生成音频，再让音箱按 URL 播放。
// blocking=true 时阻塞到播放结束——引擎逐句串行播报依赖这个语义。
// CanPreSynthesize 报告是否支持"先合成后播放"（只有豆包 TTS 支持）。
func (s *Speaker) CanPreSynthesize() bool { return s.doubaoTTSInstance() != nil }

// PlayTTSWithVoice 用指定音色/情绪合成并播放（管理台试听用，不影响配置）。
func (s *Speaker) PlayTTSWithVoice(text, speaker, emotion string) error {
	tts := s.doubaoTTSInstance()
	if tts == nil {
		return errors.New("未启用豆包语音合成")
	}
	s.ttsMu.RLock()
	baseURL := s.ttsBaseURL
	s.ttsMu.RUnlock()
	if baseURL == nil {
		return errors.New("tts base url 未就绪")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	name, err := tts.WithVoice(speaker, emotion).Synthesize(ctx, text)
	if err != nil {
		return err
	}
	base := strings.TrimRight(baseURL(), "/")
	s.ReleaseNativeSuppression()
	return s.PlayURL(base+"/tts/"+name, true)
}

// SynthesizeURL 只做语音合成，返回可播放的 URL，不播放（用配置里的默认情绪）。
func (s *Speaker) SynthesizeURL(text string) (string, error) {
	return s.SynthesizeURLVoice(text, "")
}

// SynthesizeURLVoice 同 SynthesizeURL，但可指定情绪（空 = 用配置默认）。
func (s *Speaker) SynthesizeURLVoice(text, emotion string) (string, error) {
	tts := s.doubaoTTSInstance()
	if tts == nil {
		return "", errors.New("doubao tts not configured")
	}
	s.ttsMu.RLock()
	baseURL := s.ttsBaseURL
	s.ttsMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	start := time.Now()
	name, err := tts.WithVoice("", emotion).Synthesize(ctx, text)
	if err != nil {
		return "", fmt.Errorf("doubao tts synthesize: %w", err)
	}
	log.Printf("🗣️ 豆包语音合成完成: %q (%dms)", shortenText(text, 20), time.Since(start).Milliseconds())

	if baseURL == nil {
		return "", errors.New("tts base url not ready")
	}
	base := strings.TrimRight(baseURL(), "/")
	if base == "" {
		return "", errors.New("empty tts base url")
	}
	return base + "/tts/" + name, nil
}

func (s *Speaker) playDoubaoTTS(text string, blocking bool) error {
	return s.playDoubaoTTSVoice(text, blocking, "")
}

// playDoubaoTTSVoice 同 playDoubaoTTS，可指定情绪。
func (s *Speaker) playDoubaoTTSVoice(text string, blocking bool, emotion string) error {
	run := func() error {
		tts := s.doubaoTTSInstance()
		if tts == nil {
			return errors.New("doubao tts not configured")
		}
		s.ttsMu.RLock()
		baseURL := s.ttsBaseURL
		s.ttsMu.RUnlock()

		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()

		start := time.Now()
		name, err := tts.WithVoice("", emotion).Synthesize(ctx, text)
		if err != nil {
			return fmt.Errorf("doubao tts synthesize: %w", err)
		}
		log.Printf("🗣️ 豆包语音合成完成: %q (%dms)", shortenText(text, 20), time.Since(start).Milliseconds())

		if baseURL == nil {
			return errors.New("tts base url not ready")
		}
		base := strings.TrimRight(baseURL(), "/")
		if base == "" {
			return errors.New("empty tts base url")
		}
		// 我们的音频要开播了，先解除对小爱原生播放的压制
		s.ReleaseNativeSuppression()
		return s.PlayURL(base+"/tts/"+name, true)
	}

	if blocking {
		return run()
	}
	go func() {
		if err := run(); err != nil {
			log.Printf("⚠️ 豆包 TTS 播放失败: %v", err)
		}
	}()
	return nil
}

func shortenText(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n]) + "…"
}

func (s *Speaker) PlayURL(url string, blocking bool) error {
	if blocking {
		// TTS：这是对话的一部分，灯保持亮着（8 秒不再有新动静就自动灭）
		s.showLED(8 * time.Second)
	} else {
		// 音乐：不占用指示灯
		s.HideThinkingLED()
	}
	if blocking {
		// 播报期间屏蔽服务端唤醒的自听（没有 AEC，只能"自己说话时不听"）
		s.markSpeaking(true)
		defer s.markSpeaking(false)
		script := fmt.Sprintf("miplayer -f '%s'", url)
		_, err := s.runShell(script, 10*60*1000)
		return err
	}
	script := fmt.Sprintf(`ubus -t 5 call mediaplayer player_play_url '{"url":"%s","type":1}'`, url)
	_, err := s.runShell(script, 10000)
	return err
}

// QueueMusic 记下待播歌曲（覆盖旧的）。
// 平时要等 AI 回复播完才起播（避免和确认语叠在一起）；路由等没有回复的场景
// 通过在 Speaker 上打开"立即起播"子状态来跳过等待。
func (s *Speaker) QueueMusic(url string) {
	if strings.TrimSpace(url) == "" {
		return
	}
	s.stMu.Lock()
	s.st.pendingURL = url
	s.stMu.Unlock()
}

// ClearPendingMusic 取消待播歌曲（被打断时用）。
func (s *Speaker) ClearPendingMusic() {
	s.stMu.Lock()
	s.st.pendingURL = ""
	s.stMu.Unlock()
}

// PlayPendingMusic 播放待播歌曲；没有待播就返回 nil。
func (s *Speaker) PlayPendingMusic() error {
	s.stMu.Lock()
	v := s.st.pendingURL
	s.st.pendingURL = ""
	s.stMu.Unlock()
	if v == "" {
		return nil
	}
	if err := s.PlayMusicURL(v); err != nil {
		log.Printf("⚠️ 音乐播放失败: %v", err)
		return err
	}
	return nil
}

// SetMusicEndedHandler 注入"播完自动接下一首"的回调。
// 返回 true 表示下一首已经起播，轮询继续；false 表示队列到头/失败，退出音乐态。
func (s *Speaker) SetMusicEndedHandler(fn func() bool) {
	s.stMu.Lock()
	s.st.musicEnded = fn
	s.stMu.Unlock()
}

func (s *Speaker) musicEndedHandler() func() bool {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return s.st.musicEnded
}

// MusicPlaying 当前是否在放音乐（MCP 播放的歌）。
func (s *Speaker) MusicPlaying() bool {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return s.st.music
}

// MusicPositionMs 返回本次音乐已播了多久（毫秒），没在放音乐返回 0。
// 设备对我们推的 URL 不报播放位置，只能自己计时（TTS 打断的几秒会有偏差，
// 对"判断这句话是不是当前歌词"的 ±12 秒窗口够用）。
func (s *Speaker) MusicPositionMs() int64 {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	if !s.st.music || s.st.musicStart.IsZero() {
		return 0
	}
	return time.Since(s.st.musicStart).Milliseconds()
}

// PlayPositionMs 返回当前播放位置（毫秒）；拿不到返回 0。
// 歌词比对要知道"唱到哪一句"，用设备报告的 position 就够。
func (s *Speaker) PlayPositionMs() int {
	res, err := s.runShell("ubus -t 2 call mediaplayer player_get_play_status", 3000)
	if err != nil || res == nil {
		return 0
	}
	info, ok := mediaPlayerInfo(res.Stdout)
	if !ok {
		return 0
	}
	var detail struct {
		Position int `json:"position"`
	}
	if err := json.Unmarshal(info["play_song_detail"], &detail); err != nil {
		return 0
	}
	if detail.Position < 0 {
		return 0
	}
	return detail.Position
}

// ---- 音乐降音量（ducking）----
//
// 音乐大声时麦克风收到的是被掩蔽、甚至削波后的信号，说话声不在里面，
// 事后做分离/降噪都救不回来。所以在起播前先把设备音量压到 duck 值，
// 停播/切歌结束再还原——相当于 OBS 的 sidechain ducking，只是由"播放状态"而不是
// "检测到人声"触发（没有 AEC 时后者是鸡生蛋问题）。
const defaultMusicDuckVolume = 35

// musicVolumeStep 语音"大点声/小声点"的步长。
const musicVolumeStep = 10

// voiceVolumeCeiling 音乐播放时语音调音量的上限。
// 没有 AEC，音乐超过这个音量后麦克风会被自己盖住：实测音量 30 时噪声基线
// 从 5~9k 涨到 18~20k，VAD 门槛（基线×2.2）高过说话峰值，连"豆包"都听不见。
// 想听更响就用 App 或「小爱同学，音量调到 X」走原生（原生有回声消除）。
const voiceVolumeCeiling = 25

// musicDuckFile 降音量状态的落盘路径（和 config.yaml 同目录）。
// 进程被 kill（部署/崩溃）时音量会停在 duck 值上，靠它下次连上设备后补一次还原。
var musicDuckFile string

type musicDuckRecord struct {
	Original int  `json:"original"`
	Applied  int  `json:"applied"`
	Muted    bool `json:"muted,omitempty"` // 音量 0 是我们设的（静音），重连时才能正确还原
}

// SetMusicDuckFile 设置降音量状态的落盘路径。
func (s *Speaker) SetMusicDuckFile(path string) { musicDuckFile = path }

func saveMusicDuckRecord(rec musicDuckRecord) {
	if musicDuckFile == "" {
		return
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err == nil {
		err = os.WriteFile(musicDuckFile, raw, 0o644)
	}
	if err != nil {
		log.Printf("⚠️ 保存降音量状态失败: %v", err)
	}
}

func loadMusicDuckRecord() (musicDuckRecord, bool) {
	if musicDuckFile == "" {
		return musicDuckRecord{}, false
	}
	raw, err := os.ReadFile(musicDuckFile)
	if err != nil {
		return musicDuckRecord{}, false
	}
	var rec musicDuckRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return musicDuckRecord{}, false
	}
	if rec.Original < 0 || rec.Applied < 0 {
		return musicDuckRecord{}, false
	}
	return rec, true
}

func clearMusicDuckRecord() {
	if musicDuckFile == "" {
		return
	}
	if err := os.Remove(musicDuckFile); err != nil && !os.IsNotExist(err) {
		log.Printf("⚠️ 清理降音量状态失败: %v", err)
	}
}

// SetMusicDuck 配置降音量行为，管理台保存配置后热生效。
func (s *Speaker) SetMusicDuck(enabled bool, volume int) {
	if volume <= 0 {
		volume = defaultMusicDuckVolume
	}
	if volume > 100 {
		volume = 100
	}
	s.stMu.Lock()
	s.st.duckEnabled = enabled
	s.st.duckTarget = volume
	s.stMu.Unlock()
}

// DuckMusicVolume 起播前压低音量，并记住用户原来的音量。
// 已经在降音量状态（连续切歌）时直接返回，不会把 duck 值当成"原值"再记一遍。
func (s *Speaker) DuckMusicVolume() {
	s.stMu.Lock()
	if !s.st.duckEnabled || s.st.duckOriginal >= 0 || s.st.duckBusy {
		s.stMu.Unlock()
		return
	}
	target := s.st.duckTarget
	s.st.duckBusy = true // 两次播放抢着进来时，只让第一次读"原值"
	s.stMu.Unlock()
	defer func() {
		s.stMu.Lock()
		s.st.duckBusy = false
		s.stMu.Unlock()
	}()

	cur, err := s.volume()
	if err != nil {
		log.Printf("⚠️ 读取设备音量失败，跳过降音量: %v", err)
		return
	}
	applied := target
	if cur <= target {
		applied = cur // 本来就不吵，别反过来把音量调大
	}

	// 先落记录再改音量：中途进程被 kill 也能靠记录补还原
	s.stMu.Lock()
	s.st.duckOriginal = cur
	s.st.duckApplied = applied
	s.stMu.Unlock()
	saveMusicDuckRecord(musicDuckRecord{Original: cur, Applied: applied})

	if applied != cur {
		if err := s.setVolume(applied); err != nil {
			log.Printf("⚠️ 降低音乐音量失败: %v", err)
			s.undoDuckState()
			return
		}
		log.Printf("🔉 音乐音量 %d → %d（让麦克风听得清人声）", cur, applied)
	}
}

// undoDuckState 撤销"正在降音量"的标记（改音量失败时用），并清掉落盘记录。
func (s *Speaker) undoDuckState() {
	s.stMu.Lock()
	s.st.duckOriginal = -1
	s.st.duckApplied = -1
	s.st.mutedPrev = -1
	s.stMu.Unlock()
	clearMusicDuckRecord()
}

// RestoreMusicVolume 还原用户原来的音量。
// 期间用户自己调过音量（说了"大点声"、按了按键）就不动，避免把调整覆盖掉。
func (s *Speaker) RestoreMusicVolume() {
	s.stMu.Lock()
	original, applied := s.st.duckOriginal, s.st.duckApplied
	mutedPrev := s.st.mutedPrev
	s.st.duckOriginal, s.st.duckApplied = -1, -1
	s.st.mutedPrev = -1 // 停播时静音一并解除
	s.stMu.Unlock()
	muted := mutedPrev >= 0

	if original < 0 || applied < 0 {
		// 没有进程内的降音量状态：注意**不能**清记录文件——那可能是上一个进程
		// 留下的（音量还停在 duck 值），要等连上设备补还原。
		// 但静音是我们自己设的，得放回去，否则会一直停在 0。
		if muted && mutedPrev > 0 {
			if err := s.setVolume(mutedPrev); err == nil {
				log.Printf("🔊 解除静音，音乐音量恢复为 %d", mutedPrev)
			}
		}
		return
	}
	if original == applied && !muted {
		clearMusicDuckRecord()
		return
	}
	// 静音是我们自己设的（当前音量必然不是 applied），不能当成"用户手动改过"
	if !muted {
		if cur, err := s.volume(); err == nil && cur != applied {
			log.Printf("🔊 音量已被调整为 %d，不再还原 %d", cur, original)
			clearMusicDuckRecord()
			return
		}
	}
	if err := s.setVolume(original); err != nil {
		log.Printf("⚠️ 还原音量失败: %v", err)
		return // 记录留着，下次连上设备再试
	}
	clearMusicDuckRecord()
	log.Printf("🔊 音乐音量还原为 %d", original)
}

// AdjustMusicVolume 按步长调整当前音乐音量，返回调整前后的值（"大点声/小声点"用）。
//
// 音乐处于 duck 状态时改的是 duck 值本身，并同步更新落盘记录——否则停播还原会把
// 我们自己的调整当成"用户手动改过"而跳过还原，音量就永远卡在调过的那个值上。
func (s *Speaker) AdjustMusicVolume(delta int) (int, int, error) {
	s.stMu.Lock()
	ducked := s.st.duckOriginal >= 0
	applied := s.st.duckApplied
	mutedPrev := s.st.mutedPrev
	s.stMu.Unlock()
	muted := mutedPrev >= 0

	cur := applied
	if !ducked {
		if muted {
			cur = mutedPrev // 静音中：按静音前的音量算，调音量顺带取消静音
		} else {
			v, err := s.volume()
			if err != nil {
				return 0, 0, err
			}
			cur = v
		}
	}

	next := cur + delta
	// 音乐播放中（duck 状态）语音把音量往"聋区"推时最多到 25：
	// 再高麦克风就听不见"小声点/停止"了。只在 duck 目标本身足够低时生效
	// （正常配置 20）；如果用户把 duck 音量配到 25 以上，说明自愿放弃这层保护。
	if delta > 0 && ducked && s.st.duckTarget <= voiceVolumeCeiling {
		if cur >= voiceVolumeCeiling {
			return cur, cur, nil
		}
		if next > voiceVolumeCeiling {
			next = voiceVolumeCeiling
		}
	}
	if next < 1 {
		next = 1
	}
	if next > 100 {
		next = 100
	}
	if next == cur && !muted {
		return cur, cur, nil
	}
	if err := s.setVolume(next); err != nil {
		return cur, cur, err
	}

	s.stMu.Lock()
	s.st.mutedPrev = -1 // 调音量视为顺带取消静音
	ducked = s.st.duckOriginal >= 0
	original := s.st.duckOriginal
	if ducked {
		s.st.duckApplied = next
		if next > original {
			// 比原来的正常音量还大：把它当新基准，否则停播时会被往回拉一下
			s.st.duckOriginal = next
			original = next
		}
	}
	s.stMu.Unlock()

	if ducked {
		saveMusicDuckRecord(musicDuckRecord{Original: original, Applied: next})
	}
	return cur, next, nil
}

// SetMusicMuted 静音/取消静音，返回记住的音量和是否真的动了手。
// 静音是临时的：停播还原时一并解除。
func (s *Speaker) SetMusicMuted(mute bool) (int, bool, error) {
	s.stMu.Lock()
	ducked := s.st.duckOriginal >= 0
	applied, original := s.st.duckApplied, s.st.duckOriginal
	prev := s.st.mutedPrev
	s.stMu.Unlock()

	if mute {
		if prev >= 0 {
			return prev, false, nil // 已经静音
		}
		cur := applied
		if !ducked {
			v, err := s.volume()
			if err != nil {
				return 0, false, err
			}
			cur = v
		}
		if cur <= 0 {
			return 0, false, nil
		}
		if err := s.setVolume(0); err != nil {
			return 0, false, err
		}
		s.stMu.Lock()
		s.st.mutedPrev = cur
		s.stMu.Unlock()
		if ducked {
			// 记录里带上静音标记：进程被 kill 后重连时才知道"0 是我们设的"
			saveMusicDuckRecord(musicDuckRecord{Original: original, Applied: applied, Muted: true})
		}
		return cur, true, nil
	}

	if prev < 0 {
		return 0, false, nil // 本来就没静音
	}
	target := prev
	if ducked {
		target = applied // 音乐态以 duck 值为准
	}
	if err := s.setVolume(target); err != nil {
		return 0, false, err
	}
	s.stMu.Lock()
	s.st.mutedPrev = -1
	s.stMu.Unlock()
	if ducked {
		saveMusicDuckRecord(musicDuckRecord{Original: original, Applied: applied})
	}
	return target, true, nil
}

// RestorePendingMusicDuck 处理"上次没来得及还原"的降音量：
// 部署/崩溃时进程被 kill，音量会停在 duck 值上。连上设备后补一次还原。
func (s *Speaker) RestorePendingMusicDuck() {
	rec, ok := loadMusicDuckRecord()
	if !ok {
		return
	}
	s.stMu.Lock()
	ducked := s.st.duckOriginal >= 0
	if !ducked {
		s.st.duckOriginal, s.st.duckApplied = rec.Original, rec.Applied
		if rec.Muted {
			s.st.mutedPrev = rec.Applied // 让还原逻辑知道"0 是我们压的"
		}
	}
	s.stMu.Unlock()
	if ducked {
		return // 进程内正在放音乐，记录是本次写的
	}
	log.Printf("🔊 发现上次未还原的音量（%d → %d），补一次还原", rec.Applied, rec.Original)
	s.RestoreMusicVolume()
}

func (s *Speaker) volume() (int, error) {
	if s.volumeGet != nil {
		return s.volumeGet()
	}
	return s.deviceVolume()
}

func (s *Speaker) setVolume(v int) error {
	if s.volumeSet != nil {
		return s.volumeSet(v)
	}
	return s.setDeviceVolume(v)
}

// deviceVolume 读 mediaplayer 当前音量（0-100）。
// 先问 player_get_play_status（我们本来就在轮询它），没带 volume 再退回 player_get_context。
func (s *Speaker) deviceVolume() (int, error) {
	for _, script := range []string{
		"ubus -t 2 call mediaplayer player_get_play_status",
		"ubus -t 2 call mediaplayer player_get_context",
	} {
		res, err := s.runShell(script, 3000)
		if err != nil || res == nil {
			continue
		}
		if v, ok := mediaPlayerVolume(res.Stdout); ok {
			return v, nil
		}
	}
	return 0, errors.New("设备没有返回 volume 字段")
}

// setDeviceVolume 设置音量。直调 ubus 并带 beep:0——mphelper volume_set 会"叮"一声。
func (s *Speaker) setDeviceVolume(v int) error {
	script := fmt.Sprintf(`ubus -t 2 call mediaplayer player_set_volume '{"volume":%d,"beep":0}'`, v)
	_, err := s.runShell(script, 3000)
	return err
}

// PlayMusicURL 播放音乐并进入"音乐态"。与 PlayURL 分开，避免 TTS 也走这条路。
func (s *Speaker) PlayMusicURL(url string) error {
	s.DuckMusicVolume()
	if err := s.PlayURL(url, false); err != nil {
		// 没播起来就不会有 StopMusic/trackMusic 来收尾，音量得自己还原
		s.RestoreMusicVolume()
		return err
	}
	s.stMu.Lock()
	s.st.musicStart = time.Now()
	s.st.musicURL = url
	s.stMu.Unlock()
	s.StartMusicTracking()
	return nil
}

// StopMusic 停止音乐并退出音乐态。
func (s *Speaker) StopMusic() {
	s.ClearPendingMusic()
	s.stMu.Lock()
	s.st.music = false
	s.st.musicStart = time.Time{}
	s.stMu.Unlock()
	_ = s.StopNativePlayback()
	s.RestoreMusicVolume()
}

// StartMusicTracking 轮询设备播放状态，设备停了就自动退出音乐态。
// 只允许一个轮询协程：重复调用由标记兜底（每轮先看标记）。
func (s *Speaker) StartMusicTracking() {
	s.stMu.Lock()
	s.st.music = true
	start := !s.st.musicTracking
	s.st.musicTracking = true
	s.stMu.Unlock()
	if start {
		go s.trackMusic()
	}
}

// trackMusic 轮询设备状态：一首播完自动接队列里的下一首，队列到头才退出音乐态。
//
// 轮询间隔 1s：歌曲结束后最多两秒就要接上，不然听感上就是"断了一截"。
// 起播后 5s 内不判停——设备刚接到新 URL 时状态还不稳定，容易误判成空闲。
const (
	musicEndGrace     = 5 * time.Second
	musicEndIdleTicks = 2
)

func (s *Speaker) trackMusic() {
	defer func() {
		s.stMu.Lock()
		s.st.musicTracking = false
		s.stMu.Unlock()
	}()
	idle := 0
	t := time.NewTicker(time.Second)
	defer t.Stop()
	deadline := time.Now().Add(2 * time.Hour)
	defer s.RestoreMusicVolume() // 自然播完/超时也要把音量还原
	for range t.C {
		s.stMu.Lock()
		playing, startedAt := s.st.music, s.st.musicStart
		s.stMu.Unlock()
		// 播报期间设备本来就会停，别把这个当成"音乐结束了"
		if s.Speaking() {
			idle = 0
			continue
		}
		if !playing || time.Now().After(deadline) || startedAt.IsZero() {
			s.stMu.Lock()
			s.st.music = false
			s.st.musicStart = time.Time{}
			s.stMu.Unlock()
			return
		}
		// 刚起播的几秒不判停（自动续播接下一首时尤其明显）
		if time.Since(startedAt) < musicEndGrace {
			idle = 0
			continue
		}
		res, err := s.runShell("ubus -t 2 call mediaplayer player_get_play_status", 3000)
		if err != nil || res == nil || strings.TrimSpace(res.Stdout) == "" {
			continue // 连接抖动，下一轮再看
		}
		status := mediaPlayerStatus(res.Stdout)
		if status == 1 {
			idle = 0
			continue
		}
		idle++
		if idle < musicEndIdleTicks {
			continue
		}
		// 设备“播完”实测是 status=3（不是 0）；0 是空闲。
		// 两种都算自然结束，交给队列接下一首；2=暂停（物理按键）不自动切。
		if status == 0 || status == 3 {
			if next := s.musicEndedHandler(); next != nil && next() {
				idle = 0
				continue
			}
		}
		s.stMu.Lock()
		s.st.music = false
		s.st.musicStart = time.Time{}
		s.stMu.Unlock()
		log.Printf("🎵 音乐已停止（status=%d）", status)
		return
	}
}

// mediaPlayerStatus 解析 player_get_play_status：0 空闲 / 1 播放 / 2 暂停 / 3 播完，-1 解析失败。
func mediaPlayerStatus(out string) int {
	info, ok := mediaPlayerInfo(out)
	if !ok {
		return -1
	}
	var status int
	if err := json.Unmarshal(info["status"], &status); err != nil {
		return -1
	}
	return status
}

// mediaPlayerVolume 从同一份状态输出里取 volume（0-100）。
func mediaPlayerVolume(out string) (int, bool) {
	info, ok := mediaPlayerInfo(out)
	if !ok {
		return 0, false
	}
	var volume int
	if err := json.Unmarshal(info["volume"], &volume); err != nil {
		return 0, false
	}
	return volume, true
}

// mediaPlayerInfo 解开 mediaplayer 的返回：info 既可能是对象，也可能是 JSON 字符串。
func mediaPlayerInfo(out string) (map[string]json.RawMessage, bool) {
	var outer struct {
		Info json.RawMessage `json:"info"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &outer); err != nil {
		return nil, false
	}
	raw := outer.Info
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, false
	}
	return info, true
}

type commandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func (s *Speaker) runShell(script string, timeoutMs uint64) (*commandResult, error) {
	resp, err := connect.GetRPC().CallRemote("run_shell", script, &timeoutMs)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil {
		return nil, fmt.Errorf("empty response data")
	}
	var result commandResult
	if err := json.Unmarshal(*resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}
	return &result, nil
}

func (s *Speaker) UpdateStatus(data string) {
	switch {
	case strings.Contains(data, "Playing"):
		s.Status = "playing"
	case strings.Contains(data, "Paused"):
		s.Status = "paused"
	default:
		s.Status = "idle"
	}
}

// VoicePhase 表示音箱当前的主导动作。
//
// 注意：聊天链路里的"在听/在识别"由 mic + engine 层表达，Speaker 只负责
// "我在说话 / 我在放歌"这两件对外可感知的事，所以阶段只有三种。
type VoicePhase int

const (
	PhaseIdle     VoicePhase = iota // 安静
	PhaseSpeaking                   // 正在播报（含 700ms 冷却）
	PhaseMusic                      // 正在放音乐
)

func (p VoicePhase) String() string {
	switch p {
	case PhaseSpeaking:
		return "speaking"
	case PhaseMusic:
		return "music"
	default:
		return "idle"
	}
}

// Phase 返回当前主导阶段：说话优先于放歌（播报时音乐若有也会被当作说话态处理）。
func (s *Speaker) Phase() VoicePhase {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	if s.st.speaking || time.Now().Before(s.st.speakingCool) {
		return PhaseSpeaking
	}
	if s.st.music {
		return PhaseMusic
	}
	return PhaseIdle
}

// BeginImmediatePlay / EndImmediatePlay：标记"这段时间没有 AI 回复"，
// 这期间点歌要立刻起播，而不是排队等回复播完（音乐态路由就是这种情况）。
func (s *Speaker) BeginImmediatePlay() {
	s.stMu.Lock()
	s.st.immediatePlay = true
	s.stMu.Unlock()
}

func (s *Speaker) EndImmediatePlay() {
	s.stMu.Lock()
	s.st.immediatePlay = false
	s.stMu.Unlock()
}

// ImmediatePlayActive 当前是否处于"点歌立刻起播"的窗口。
func (s *Speaker) ImmediatePlayActive() bool {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return s.st.immediatePlay
}

// stateSnapshot 返回当前状态的副本，只给测试和调试用。
// 生产代码请走语义方法，不要基于快照做判断后再回写（会破坏不变量）。
func (s *Speaker) stateSnapshot() playState {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return s.st
}
