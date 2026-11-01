package main

// 原生唤醒接管：音乐态下把「小爱同学」这一轮对话交给豆包。
//
// 做法是把设备原生播放器 mediaplayer 冻住（SIGSTOP）——原生 TTS 和音乐一起停，
// 而我们自己的 TTS 走独立进程 miplayer，照常出声。实测见
// docs/device-audio-aec-native-asr.md 的「原生唤醒接管」一节。

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
	// nativeTakeoverWatch 冻结期间的复核间隔。实验里出现过 mediaplayer 被外部 SIGCONT。
	nativeTakeoverWatch = 300 * time.Millisecond
	// nativeTakeoverRefreezeFails 连续补冻失败多少次后放弃接管。
	nativeTakeoverRefreezeFails = 2
)

var nativeTakeoverRPCTimeoutMs uint64 = 3000

type nativeTakeoverState struct {
	mu       sync.Mutex
	active   bool
	pid      int
	deadline time.Time
	timer    *time.Timer
	stopCh   chan struct{}
	failures int
	// musicOn 进入接管时是否在放歌；结束时要恢复
	musicOn bool
}

// TakeoverActive 报告当前是否处于原生唤醒接管中。
func (s *Speaker) TakeoverActive() bool {
	s.nativeTakeover.mu.Lock()
	defer s.nativeTakeover.mu.Unlock()
	return s.nativeTakeover.active
}

// BeginNativeTakeover 冻结设备播放器并进入接管状态。重复调用是幂等的。
func (s *Speaker) BeginNativeTakeover(max time.Duration) error {
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

	pid, err := s.freezeNative()
	if err != nil {
		return err
	}
	if pid <= 0 {
		return fmt.Errorf("freeze_native 没有返回 pid")
	}

	t.mu.Lock()
	t.active = true
	t.pid = pid
	t.failures = 0
	t.musicOn = s.MusicPlaying()
	t.deadline = time.Now().Add(max)
	t.stopCh = make(chan struct{})
	stop := t.stopCh
	t.timer = time.AfterFunc(max, func() { s.EndNativeTakeover("超时") })
	t.mu.Unlock()

	go s.watchNativeTakeover(stop)
	log.Printf("❄️ 原生唤醒接管开始：已冻结 mediaplayer(pid=%d)，上限 %v", pid, max)
	return nil
}

// EndNativeTakeover 解冻设备播放器并退出接管状态；幂等。
func (s *Speaker) EndNativeTakeover(reason string) {
	t := &s.nativeTakeover
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return
	}
	t.active = false
	pid := t.pid
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

	// 按进程名解冻：冻结期间 mediaplayer 可能被守护进程重启，存的 pid 会过期
	log.Printf("☀️ 原生唤醒接管结束（%s）：清理原生播放器(pid=%d)", reason, pid)
	// 杀掉被冻住的播放器清空队列（解冻再 stop 会晚一拍，小爱会冒一声）
	if s.nativeReset != nil {
		go s.nativeReset(musicOn)
	} else {
		go s.resetPlaybackAfterTakeover(musicOn)
	}
}

// watchNativeTakeover 在冻结期间盯着播放器：被外部解冻就补一刀，进程没了就结束接管。
func (s *Speaker) watchNativeTakeover(stop <-chan struct{}) {
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
				// 播放器被守护进程重启/消失，冻结已经没有意义
				s.EndNativeTakeover("播放器已不存在")
				return
			}
			if stopped {
				s.setTakeoverFailures(0)
				continue
			}
			newPid, err := s.freezeNative()
			if err != nil {
				if s.bumpTakeoverFailures() >= nativeTakeoverRefreezeFails {
					s.EndNativeTakeover("补冻失败")
					return
				}
				continue
			}
			s.setTakeoverFailures(0)
			s.setTakeoverPid(newPid)
			log.Printf("❄️ 播放器被外部解冻，已重新冻结(pid=%d)", newPid)
		}
	}
}

// resetPlaybackAfterTakeover 结束接管时清掉原生播放队列。
//
// 不能"先解冻再 stop"：解冻那一刻排队的原生 TTS 已经开始出声，stop 晚 100ms 就晚了。
// 这里直接杀掉被冻住的 mediaplayer，procd 会拉起一个干净实例，队列自然没了；
// 放歌的话再把当前曲目重新拉起来。
func (s *Speaker) resetPlaybackAfterTakeover(musicOn bool) {
	oldPID, _, _ := s.nativePlayerState()
	if _, err := s.runShell(`kill -9 $(pidof mediaplayer) 2>/dev/null; true`, 4000); err != nil {
		log.Printf("⚠️ 结束接管时杀掉原生播放器失败: %v", err)
	}

	newPID := 0
	for i := 0; i < 15; i++ {
		time.Sleep(200 * time.Millisecond)
		pid, _, err := s.nativePlayerState()
		if err == nil && pid > 0 && pid != oldPID {
			newPID = pid
			break
		}
	}
	if newPID == 0 {
		// 没被拉起：至少把旧进程解冻，别让它永远停在 T
		log.Printf("⚠️ 原生播放器没有重新拉起（旧 pid=%d），退回解冻", oldPID)
		_ = s.unfreezeNative(0)
	} else {
		log.Printf("♻️ 原生播放器已重启（%d → %d），排队的原生 TTS 已丢弃", oldPID, newPID)
	}
	if musicOn {
		s.resumeMusicAfterSpeech()
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

// freezeNative 让客户端冻结 mediaplayer，返回它的 pid。
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

// ContinueNativeDialog 让固件原生助手重新开一次收音，用于「小爱同学」之后的连续对话。
// 原生 VAD 判停（空闲约 6 秒），识别结果仍从 instruction.log 进来。
func (s *Speaker) ContinueNativeDialog() error {
	if _, err := connect.GetRPC().CallRemote("native_continue_dialog", nil, &nativeTakeoverRPCTimeoutMs); err != nil {
		return fmt.Errorf("native_continue_dialog 调用失败: %w", err)
	}
	return nil
}

func (s *Speaker) nativePlayerState() (int, bool, error) {
	if s.nativeStateFn != nil {
		return s.nativeStateFn()
	}
	return s.nativePlayerStateRPC()
}

// freezeNativePlayerRPC 让客户端冻结 mediaplayer，返回它的 pid。
func (s *Speaker) freezeNativePlayerRPC() (int, error) {
	resp, err := connect.GetRPC().CallRemote("freeze_native", nil, &nativeTakeoverRPCTimeoutMs)
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
	payload := map[string]int{"pid": pid}
	if _, err := connect.GetRPC().CallRemote("unfreeze_native", payload, &nativeTakeoverRPCTimeoutMs); err != nil {
		return fmt.Errorf("unfreeze_native 调用失败: %w", err)
	}
	return nil
}

// nativePlayerStateRPC 查询 mediaplayer 的 pid 与冻结状态。
func (s *Speaker) nativePlayerStateRPC() (int, bool, error) {
	resp, err := connect.GetRPC().CallRemote("native_playback_state", nil, &nativeTakeoverRPCTimeoutMs)
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
