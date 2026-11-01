//go:build !linux

package utils

import "fmt"

// 非 linux 平台（服务端在 Windows/macOS 构建时会编译到本包）只需要能编过；
// 真正的冻结/解冻只在音箱端调用。

func FreezeNativePlayback() (int, error) {
	return 0, fmt.Errorf("冻结原生播放器仅支持 linux")
}

func UnfreezeNativePlayback(int) error {
	return fmt.Errorf("解冻原生播放器仅支持 linux")
}
