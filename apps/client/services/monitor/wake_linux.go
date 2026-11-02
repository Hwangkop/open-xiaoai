//go:build linux

package monitor

import (
	"path/filepath"
	"syscall"
	"time"
)

// newFileWake 用 inotify 把"文件有新内容"的发现延迟从 100ms 轮询降到毫秒级。
//
// 原生 ASR 的 final 和它的 TTS 请求几乎同时发生，晚 100ms 再冻结就来不及了。
// 监听文件本身 + 所在目录（覆盖日志轮转/重建）；失败时返回 nil，调用方退回轮询。
func newFileWake(path string) (<-chan struct{}, func()) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, func() {}
	}

	const mask = syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE | syscall.IN_CREATE |
		syscall.IN_MOVED_TO | syscall.IN_ATTRIB | syscall.IN_DELETE_SELF

	// 目录 watch 用于捕获日志轮转/重建；文件 watch 用于内容写入。
	_, _ = syscall.InotifyAddWatch(fd, filepath.Dir(path), mask)
	_, _ = syscall.InotifyAddWatch(fd, path, mask)

	wake := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := syscall.Read(fd, buf)
			if err != nil {
				if err == syscall.EAGAIN || err == syscall.EINTR {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				return
			}
			if n > 0 {
				select {
				case wake <- struct{}{}:
				default: // 已有待处理唤醒，合并
				}
			}
		}
	}()

	return wake, func() {
		close(done)
		_ = syscall.Close(fd)
	}
}
