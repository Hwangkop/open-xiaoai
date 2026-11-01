//go:build linux

package utils

import (
	"fmt"
	"syscall"
)

// FreezeNativePlayback 找到 mediaplayer 并发 SIGSTOP，返回它的 pid。
func FreezeNativePlayback() (int, error) {
	pid, err := FindProcessByName(NativePlayerName)
	if err != nil {
		return 0, err
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return 0, fmt.Errorf("冻结 %s(pid=%d) 失败: %w", NativePlayerName, pid, err)
	}
	return pid, nil
}

// UnfreezeNativePlayback 对 pid 发 SIGCONT。pid<=0 时按名字查找；
// 进程已经退出视为成功（幂等）。
func UnfreezeNativePlayback(pid int) error {
	if pid <= 0 {
		found, err := FindProcessByName(NativePlayerName)
		if err != nil {
			return nil // 已经没了，无需解冻
		}
		pid = found
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return fmt.Errorf("解冻 pid=%d 失败: %w", pid, err)
	}
	return nil
}
