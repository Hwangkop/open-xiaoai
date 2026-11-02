package main

// 原生唤醒接管：把「小爱同学」这一轮对话交给豆包。
//
// instruction 模式在 SDK 派发前按 dialog 阻止原生动作，aivs 和音乐保持运行；
// freeze 兼容模式暂停 mico_aivs_lab，并事后尝试停止原生 TTS，仍有漏音风险。
// 豆包 WAV 使用 notify PCM 混音。实测、边界与回滚见 tools/native-guard/README.md。

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
)

const (
	// nativeTakeoverDefaultMax 单轮接管上限：到点强制解冻，避免音乐永久停住。
	nativeTakeoverDefaultMax = 25 * time.Second
	// nativeTakeoverWatch 冻结期间的复核间隔。实验里出现过原生助手被外部 SIGCONT。
	nativeTakeoverWatch = 300 * time.Millisecond
	// nativeTakeoverRefreezeFails 连续补冻失败多少次后放弃接管。
	nativeTakeoverRefreezeFails = 2
)

var nativeTakeoverRPCTimeoutMs uint64 = 3000

// nativeTakeoverProcess 冻结谁：冻 aivs 让它生成不了回答，播放器不动，
// 音乐可以一直放（我们的 TTS 走 aplay 的 dmix，和音乐混音）。
const nativeTakeoverProcess = "mico_aivs_lab"

type nativeTakeoverState struct {
	mu          sync.Mutex
	active      bool
	instruction bool
	pid         int
	epoch       uint64 // 接管轮次；旧轮次的收尾不能结束新轮次
	deadline    time.Time
	timer       *time.Timer
	stopCh      chan struct{}
	failures    int
	// musicOn 进入接管时是否在放歌；结束时要恢复
	musicOn bool
}

// TakeoverActive 报告当前是否处于原生唤醒接管中。
func (s *Speaker) TakeoverActive() bool {
	s.nativeTakeover.mu.Lock()
	defer s.nativeTakeover.mu.Unlock()
	return s.nativeTakeover.active
}

// TakeoverEpoch 返回当前接管轮次；没有接管时为 0。
// 引擎在整轮回复结束时用 epoch 收尾，避免打断后旧轮次误结束新轮次。
func (s *Speaker) TakeoverEpoch() uint64 {
	t := &s.nativeTakeover
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return 0
	}
	return t.epoch
}

// EndNativeTakeoverFor 只结束指定轮次的接管。
func (s *Speaker) EndNativeTakeoverFor(epoch uint64, reason string) {
	t := &s.nativeTakeover
	t.mu.Lock()
	active, same := t.active, t.epoch == epoch
	t.mu.Unlock()
	if !active || !same {
		return
	}
	s.EndNativeTakeover(reason)
}

// BeginNativeTakeover 进入整轮接管；只有已认领的 guarded 轮次才能跳过冻结。
func (s *Speaker) BeginNativeTakeover(max time.Duration, guarded ...bool) error {
	if max <= 0 {
		max = nativeTakeoverDefaultMax
	}

	t := &s.nativeTakeover
	t.mu.Lock()
	if t.active {
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()

	// 新一轮接管取消上一轮的“解冻后兜底”窗口，避免旧 goroutine 继续清队列。
	s.lateNativeTTSUntil.Store(0)

	musicOn := s.MusicPlaying()
	instruction := len(guarded) > 0 && guarded[0]
	pid := 0
	if !instruction {
		var err error
		pid, err = s.freezeNative()
		if err != nil {
			return err
		}
		if pid <= 0 {
			return fmt.Errorf("freeze_native 没有返回 pid")
		}
	}

	t.mu.Lock()
	t.active = true
	t.instruction = instruction
	t.epoch++
	epoch := t.epoch
	t.pid = pid
	t.failures = 0
	t.musicOn = musicOn
	t.deadline = time.Now().Add(max)
	t.stopCh = make(chan struct{})
	stop := t.stopCh
	t.timer = time.AfterFunc(max, func() { s.EndNativeTakeoverFor(epoch, "超时") })
	t.mu.Unlock()

	// 原生 dialog 会把播放器停下来听；从这一刻起，设备空闲不是"歌播完了"。
	s.BeginNativeASRWindow()
	// 原生 TTS 走 media=mibrain 的独立队列；清掉它不会动 common 上的音乐。
	// 即使 aivs 已经抢在我们冻结前把 TTS 请求发出去了，这一步也能把队列清空。
	if !instruction {
		s.StopNativeTTS("接管开始")
	}

	// 原生 dialog 开麦时通常已经把 mediaplayer 停掉了；此时 aivs 已冻结，
	// 趁没人再抢播放器把当前曲目接回来，思考/豆包 TTS 期间音乐也能继续。
	// 只在接管前确实在放歌时做，避免把用户已经停掉的歌重新放出来。
	if musicOn {
		go s.resumeMusicAfterSpeech()
	}

	if instruction {
		log.Printf("🛡️ 原生唤醒接管开始：本轮已由 SDK 指令钩子认领，music=%v", musicOn)
		return nil
	}
	go s.watchNativeTakeover(stop, epoch)
	log.Printf("❄️ 原生唤醒接管开始：已冻结 %s(pid=%d)，music=%v，上限 %v", nativeTakeoverProcess, pid, musicOn, max)
	return nil
}

// EndNativeTakeover 退出整轮接管；指令隔离保留墓碑，兼容模式解冻 aivs。
func (s *Speaker) EndNativeTakeover(reason string) {
	t := &s.nativeTakeover
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return
	}
	t.active = false
	pid := t.pid
	instruction := t.instruction
	t.instruction = false
	musicOn := t.musicOn
	t.musicOn = false
	t.pid = 0
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	if t.stopCh != nil {
		close(t.stopCh)
		t.stopCh = nil
	}
	t.mu.Unlock()

	if instruction {
		log.Printf("🛡️ 原生唤醒接管结束（%s）：保留本轮指令墓碑，不发解冻/清队列命令", reason)
		s.EndNativeASRWindow()
		return
	}
	// 解冻前先标记兜底窗口，避免 Speak 在 RPC 返回之前到达而被放行。
	// 这是事后补救，仍不能保证已排队的 TTS 没有首音节漏出。
	s.startLateNativeTTSSuppression(3 * time.Second)
	// 只解冻 aivs；不要重启它。实测重启 aivs 会重置音频链路，
	// 把仍在播的豆包 TTS 和暂停中的音乐一起带走。
	_ = s.unfreezeNative(0)
	log.Printf("☀️ 原生唤醒接管结束（%s）：已解冻 %s(pid=%d)，music=%v", reason, nativeTakeoverProcess, pid, musicOn)

	// 原生 dialog 可能在 ASR 阶段就把播放器停了（status=0）。
	// 关窗口时尝试 mphelper play 原位置续播，失败才重拉 URL。
	s.EndNativeASRWindow()
}

// watchNativeTakeover 在冻结期间盯着播放器：被外部解冻就补一刀，进程没了就结束接管。
func (s *Speaker) watchNativeTakeover(stop <-chan struct{}, epoch uint64) {
	tk := time.NewTicker(nativeTakeoverWatch)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			pid, stopped, err := s.nativePlayerState()
			if err != nil {
				continue
			}
			if pid == 0 {
				// 原生助手被守护进程重启/消失，冻结已经没有意义
				s.EndNativeTakeoverFor(epoch, "原生助手已不存在")
				return
			}
			if stopped {
				s.setTakeoverFailures(0)
				continue
			}
			newPid, err := s.freezeNative()
			if err != nil {
				if s.bumpTakeoverFailures() >= nativeTakeoverRefreezeFails {
					s.EndNativeTakeoverFor(epoch, "补冻失败")
					return
				}
				continue
			}
			s.setTakeoverFailures(0)
			s.setTakeoverPid(newPid)
			log.Printf("❄️ 原生助手被外部解冻，已重新冻结(pid=%d)", newPid)
		}
	}
}

func (s *Speaker) setTakeoverPid(pid int) {
	s.nativeTakeover.mu.Lock()
	if pid > 0 {
		s.nativeTakeover.pid = pid
	}
	s.nativeTakeover.mu.Unlock()
}

func (s *Speaker) setTakeoverFailures(n int) {
	s.nativeTakeover.mu.Lock()
	s.nativeTakeover.failures = n
	s.nativeTakeover.mu.Unlock()
}

func (s *Speaker) bumpTakeoverFailures() int {
	s.nativeTakeover.mu.Lock()
	defer s.nativeTakeover.mu.Unlock()
	s.nativeTakeover.failures++
	return s.nativeTakeover.failures
}

// freezeNative 让客户端冻结 mico_aivs_lab，返回它的 pid。
// 测试里可以替换 Speaker.nativeFreeze。
func (s *Speaker) freezeNative() (int, error) {
	if s.nativeFreeze != nil {
		return s.nativeFreeze()
	}
	return s.freezeNativePlayerRPC()
}

func (s *Speaker) unfreezeNative(pid int) error {
	if s.nativeUnfreeze != nil {
		return s.nativeUnfreeze(pid)
	}
	return s.unfreezeNativePlayerRPC(pid)
}

// LateNativeTTSSuppressed 解冻后是否仍在“防迟到原生 TTS”的兜底窗口内。
func (s *Speaker) LateNativeTTSSuppressed() bool {
	return time.Now().UnixMilli() < s.lateNativeTTSUntil.Load()
}

// startLateNativeTTSSuppression 解冻后定时清 mibrain，抓 aivs 补发的原生 TTS。
// 只清 mibrain 队列，不碰 common 上的音乐。
func (s *Speaker) startLateNativeTTSSuppression(d time.Duration) {
	if d <= 0 {
		return
	}
	until := time.Now().Add(d).UnixMilli()
	s.lateNativeTTSUntil.Store(until)
	go func() {
		start := time.Now()
		// 0ms 先清一次；aivs 补发通常在一两百毫秒内，再补几次覆盖慢路径。
		for _, after := range []time.Duration{
			0, 150 * time.Millisecond, 500 * time.Millisecond,
			1500 * time.Millisecond, 3 * time.Second,
		} {
			if wait := after - time.Since(start); wait > 0 {
				time.Sleep(wait)
			}
			if s.lateNativeTTSUntil.Load() != until {
				return
			}
			s.StopNativeTTS("解冻后兜底")
		}
	}()
}

// StopNativeTTS 请求停止原生 TTS（media=mibrain）。
//
// media 参数不保证播放器状态隔离；请求返回成功也不能证明没有可听漏音。
// 我们的 TTS 走 aplay/miplayer，不经过此命令。
func (s *Speaker) StopNativeTTS(reason string) {
	s.nativeTakeover.mu.Lock()
	fallback := s.nativeTakeover.active && !s.nativeTakeover.instruction
	s.nativeTakeover.mu.Unlock()
	if s.nativeInstructionIsolation.Load() && !fallback && !s.LateNativeTTSSuppressed() {
		return
	}

	if s.nativeStopTTS != nil {
		s.nativeStopTTS(reason)
		return
	}
	script := `ubus -t 2 call mediaplayer player_play_operation '{"action":"stop","media":"mibrain"}'`
	res, err := s.runShell(script, 3000)
	if err != nil {
		log.Printf("⚠️ [native-tts] 清空 mibrain 队列失败（%s）: %v", reason, err)
		return
	}
	if res == nil || res.ExitCode != 0 {
		log.Printf("⚠️ [native-tts] 停止 mibrain 命令执行失败（%s）", reason)
		return
	}
	var reply struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal([]byte(res.Stdout), &reply) != nil || reply.Code == nil || *reply.Code != 0 {
		log.Printf("⚠️ [native-tts] 停止 mibrain 未收到成功确认（%s）", reason)
		return
	}
	log.Printf("🔇 [native-tts] mibrain stop 已确认（%s）；声学隔离仍需实测", reason)
}

// SyncNativeGuard 同步隔离方式与原生透传关键词，并记录客户端实际生效方式。
func (s *Speaker) SyncNativeGuard(cfg *AppConfig) {
	if cfg == nil {
		return
	}
	mode := cfg.NativeWake.IsolationMode
	if mode == "" {
		mode = "freeze"
	}
	payload := map[string]any{"enabled": cfg.NativeWake.Enabled, "mode": mode, "native_keywords": cfg.NativeKeywords}
	resp, err := connect.GetRPC().CallRemote("native_guard", payload, &nativeTakeoverRPCTimeoutMs)
	if err != nil || (resp.Code != nil && *resp.Code != 0) {
		log.Printf("⚠️ 同步原生隔离策略失败：%v", err)
		return
	}
	var result struct {
		Mode    string `json:"mode"`
		Enabled bool   `json:"enabled"`
	}
	if resp.Data != nil {
		_ = json.Unmarshal(*resp.Data, &result)
	}
	s.nativeInstructionIsolation.Store(result.Enabled && result.Mode == "instruction")
	log.Printf("🛡️ 原生隔离策略：requested=%s effective=%s enabled=%v", mode, result.Mode, cfg.NativeWake.Enabled)
}

// ReleaseNativePreFreeze 客户端已本地预冻结，但本轮不接管时立刻放行。
func (s *Speaker) ReleaseNativePreFreeze(reason string) {
	if s.TakeoverActive() {
		return
	}
	if _, err := connect.GetRPC().CallRemote("native_guard_release", map[string]string{"reason": reason}, &nativeTakeoverRPCTimeoutMs); err != nil {
		log.Printf("⚠️ 释放本地预冻结失败（%s）: %v", reason, err)
		return
	}
	log.Printf("☀️ 已释放本地预冻结（%s）", reason)
}

// ContinueNativeDialog 让固件原生助手重新开一次收音，用于「小爱同学」之后的连续对话。
// 原生 VAD 判停（空闲约 6 秒），识别结果仍从 instruction.log 进来。
func (s *Speaker) ContinueNativeDialog() error {
	// 原生服务暂不可用时有限重试，不重启 aivs 或音频服务。
	var lastErr error
	for i := 0; i < 3; i++ {
		if _, err := connect.GetRPC().CallRemote("native_continue_dialog", nil, &nativeTakeoverRPCTimeoutMs); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("native_continue_dialog 调用失败: %w", lastErr)
}

func (s *Speaker) nativePlayerState() (int, bool, error) {
	if s.nativeStateFn != nil {
		return s.nativeStateFn()
	}
	return s.nativePlayerStateRPC()
}

// freezeNativePlayerRPC 让客户端冻结 mediaplayer，返回它的 pid。
func (s *Speaker) freezeNativePlayerRPC() (int, error) {
	resp, err := connect.GetRPC().CallRemote("freeze_native", map[string]string{"name": nativeTakeoverProcess}, &nativeTakeoverRPCTimeoutMs)
	if err != nil {
		return 0, fmt.Errorf("freeze_native 调用失败: %w", err)
	}
	if resp.Data == nil {
		return 0, fmt.Errorf("freeze_native 返回空数据")
	}
	var out struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(*resp.Data, &out); err != nil {
		return 0, fmt.Errorf("解析 freeze_native 结果失败: %w", err)
	}
	return out.PID, nil
}

// unfreezeNativePlayerRPC 解冻指定 pid；pid<=0 时由客户端按进程名查找。
func (s *Speaker) unfreezeNativePlayerRPC(pid int) error {
	payload := map[string]any{"pid": pid, "name": nativeTakeoverProcess}
	if _, err := connect.GetRPC().CallRemote("unfreeze_native", payload, &nativeTakeoverRPCTimeoutMs); err != nil {
		return fmt.Errorf("unfreeze_native 调用失败: %w", err)
	}
	return nil
}

// nativePlayerStateRPC 查询 mediaplayer 的 pid 与冻结状态。
func (s *Speaker) nativePlayerStateRPC() (int, bool, error) {
	resp, err := connect.GetRPC().CallRemote("native_playback_state", map[string]string{"name": nativeTakeoverProcess}, &nativeTakeoverRPCTimeoutMs)
	if err != nil {
		return 0, false, err
	}
	if resp.Data == nil {
		return 0, false, fmt.Errorf("native_playback_state 返回空数据")
	}
	var out struct {
		PID     int  `json:"pid"`
		Stopped bool `json:"stopped"`
	}
	if err := json.Unmarshal(*resp.Data, &out); err != nil {
		return 0, false, err
	}
	return out.PID, out.Stopped, nil
}
