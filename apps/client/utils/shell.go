package utils

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxOutputBytes 限制 shell 输出大小，防止恶意/异常脚本导致老设备 OOM
const maxOutputBytes = 512 * 1024

// limitedBuffer 在达到 max 后静默截断，避免内存膨胀
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (n int, err error) {
	remain := l.max - l.buf.Len()
	if remain <= 0 {
		return len(p), nil // 已满，丢弃后续写入
	}
	if len(p) > remain {
		p = p[:remain]
	}
	return l.buf.Write(p)
}

func (l *limitedBuffer) String() string { return l.buf.String() }

type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func RunShell(script string) (*CommandResult, error) {
	return RunShellWithTimeout(script, 10*time.Second)
}

func RunShellWithTimeout(script string, timeout time.Duration) (*CommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", script)

	stdout := &limitedBuffer{max: maxOutputBytes}
	stderr := &limitedBuffer{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	return &CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// TTSRunner runs TTS/play scripts that can be interrupted by StopTTS.
var ttsRunner struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	cancelID int64
	ducking  bool
}

var ttsCancelID int64

// 注意：mediaplayer 没有 player_stop 方法（早期写的那个调用一直是静默失败），
// 真正可用的是 player_play_operation，action 取 pause/stop。
const stopNativePlaybackScript = `
mphelper pause >/dev/null 2>&1 || true
ubus call mediaplayer player_play_operation '{"action":"pause"}' >/dev/null 2>&1 || true
ubus call mediaplayer player_play_operation '{"action":"stop"}' >/dev/null 2>&1 || true
pkill -f 'tts_play.sh' >/dev/null 2>&1 || true
`

const (
	notifyTTSStartScript = `ubus call pnshelper event_notify '{"src":3,"event":12}' >/dev/null 2>&1 || true`
	notifyTTSEndScript   = `ubus call pnshelper event_notify '{"src":3,"event":13}' >/dev/null 2>&1 || true`
)

var stopNativePlayback = func(script string) {
	_, _ = RunShellWithTimeout(script, 3*time.Second)
}

// suppressUntil 压制原生播放的截止时间（unix ms）。AI 接管后小爱可能晚一拍才开口，
// 单次 stop 拦不住，需要在一个短窗口内反复压。我们自己开始播放时立刻解除。
var suppressUntil atomic.Int64

// SuppressNativePlayback 在 d 时间窗内反复停掉固件原生播放。
// d <= 0 表示立刻解除压制（例如我们自己的音频要开始播了）。
func SuppressNativePlayback(d time.Duration) {
	if d <= 0 {
		suppressUntil.Store(0)
		return
	}
	suppressUntil.Store(time.Now().Add(d).UnixMilli())
	go func() {
		for {
			if time.Now().UnixMilli() >= suppressUntil.Load() {
				return
			}
			stopNativePlayback(stopNativePlaybackScript)
			time.Sleep(300 * time.Millisecond)
		}
	}()
}

func cancelNativeSuppression() {
	suppressUntil.Store(0)
}

// StopNativePlayback 无条件停掉固件自己的播放（小爱原生回复 / 音乐）。
//
// 与 StopTTS 的差别：StopTTS 只在本进程发起过播放时才动原生播放器，
// 避免误伤后台音乐；但"小爱先答、我们再让豆包答"的场景下本进程没发起过，
// 于是两套声音会叠在一起。这个函数专用于接管场景，直接停。
func StopNativePlayback() {
	stopNativePlayback(stopNativePlaybackScript)
}

var notifyTTSStart = func() {
	_, _ = RunShellWithTimeout(notifyTTSStartScript, 2*time.Second)
}

var notifyTTSEnd = func() {
	_, _ = RunShellWithTimeout(notifyTTSEndScript, 2*time.Second)
}

func isTTSScript(script string) bool {
	return strings.Contains(script, "tts_play.sh")
}

// RunShellInterruptible runs tts_play.sh or miplayer scripts; can be stopped via StopTTS.
func RunShellInterruptible(script string, timeout time.Duration) (*CommandResult, error) {
	// 我们自己的播放开始了，不需要再压制原生播放（否则会把自己也停掉）
	cancelNativeSuppression()
	duck := isTTSScript(script)
	var endPreviousDuck bool

	ttsRunner.mu.Lock()
	if ttsRunner.cancel != nil {
		ttsRunner.cancel()
		ttsRunner.cancel = nil
	}
	if ttsRunner.ducking && !duck {
		endPreviousDuck = true
		ttsRunner.ducking = false
	}
	ttsCancelID++
	myID := ttsCancelID
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ttsRunner.cancel = cancel
	ttsRunner.cancelID = myID
	ttsRunner.ducking = duck
	ttsRunner.mu.Unlock()

	if endPreviousDuck {
		notifyTTSEnd()
	}
	if duck {
		notifyTTSStart()
	}

	defer func() {
		var endDuck bool

		ttsRunner.mu.Lock()
		if ttsRunner.cancelID == myID {
			ttsRunner.cancel = nil
			if ttsRunner.ducking && duck {
				endDuck = true
				ttsRunner.ducking = false
			}
		}
		ttsRunner.mu.Unlock()

		if endDuck {
			notifyTTSEnd()
		}
		cancel()
	}()

	cmd := exec.CommandContext(ctx, "sh", "-c", script)

	stdout := &limitedBuffer{max: maxOutputBytes}
	stderr := &limitedBuffer{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.Canceled {
			exitCode = -1
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	return &CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// StopTTS cancels the current RunShellInterruptible playback. It only sends
// native stop commands when this process has managed playback in flight, so a
// fresh AI request does not pause unrelated background music.
func StopTTS() {
	var endDuck bool
	var hadManagedPlayback bool

	ttsRunner.mu.Lock()
	if ttsRunner.cancel != nil {
		hadManagedPlayback = true
		ttsRunner.cancel()
		ttsRunner.cancel = nil
	}
	if ttsRunner.ducking {
		hadManagedPlayback = true
		endDuck = true
		ttsRunner.ducking = false
	}
	ttsRunner.mu.Unlock()
	if endDuck {
		notifyTTSEnd()
	}
	if hadManagedPlayback {
		// 只确保 tts_play.sh 退出；不要再全量 stop mediaplayer。
		// 原生 TTS 由服务端 stop media:"mibrain" 处理，全量 stop 会把背景音乐一起清掉。
		stopNativePlayback(`pkill -f 'tts_play.sh' >/dev/null 2>&1 || true`)
	}
}
