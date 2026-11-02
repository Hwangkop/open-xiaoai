//go:build !linux

package monitor

// newFileWake 在非 Linux 平台没有 inotify，返回 nil 让调用方退回轮询。
func newFileWake(string) (<-chan struct{}, func()) {
	return nil, func() {}
}
