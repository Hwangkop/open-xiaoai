package utils

// 原生播放器（mediaplayer）的进程控制。
//
// 音乐和原生 TTS 都走 mediaplayer，而我们的 TTS 走独立进程 miplayer。
// 接管原生唤醒时把 mediaplayer SIGSTOP，就能同时挡住原生回答和音乐，
// 又不影响我们自己的播报。详见 docs/device-audio-aec-native-asr.md。

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// NativePlayerName 是固件原生播放器的进程名。
const NativePlayerName = "mediaplayer"

// FindProcessByName 在 /proc 里按 comm 精确匹配进程名，返回第一个命中的 pid。
func FindProcessByName(name string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("读取 /proc 失败: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == name {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("找不到进程 %s", name)
}

// ProcessState 返回 /proc/<pid>/stat 里的状态字符（R/S/T/Z...）。
// stat 的第二个字段 comm 可能含空格和括号，所以从最后一个 ')' 之后开始解析。
func ProcessState(pid int) (byte, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	return parseStatState(string(data))
}

// parseStatState 从一行 /proc/<pid>/stat 里取出状态字符。
func parseStatState(stat string) (byte, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return 0, fmt.Errorf("无法解析 stat: %q", stat)
	}
	rest := strings.TrimLeft(stat[i+1:], " ")
	if rest == "" {
		return 0, fmt.Errorf("无法解析 stat: %q", stat)
	}
	return rest[0], nil
}

// IsProcessStopped 判断进程是否处于停止态（T/t）。
func IsProcessStopped(pid int) bool {
	st, err := ProcessState(pid)
	if err != nil {
		return false
	}
	return st == 'T' || st == 't'
}

// NativeProcessState 返回指定进程的 pid 与是否处于停止态。
func NativeProcessState(name string) (pid int, stopped bool) {
	pid, err := FindProcessByName(name)
	if err != nil {
		return 0, false
	}
	return pid, IsProcessStopped(pid)
}

// NativePlaybackState 返回 mediaplayer 的 pid 与是否处于冻结态。
// 进程不存在时返回 pid=0、stopped=false。
func NativePlaybackState() (pid int, stopped bool) {
	pid, err := FindProcessByName(NativePlayerName)
	if err != nil {
		return 0, false
	}
	return pid, IsProcessStopped(pid)
}
