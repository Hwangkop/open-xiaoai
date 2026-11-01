package netease

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLyricLive 打真实接口，需要环境变量 NETEASE_COOKIE（登录态），否则跳过。
// 用法：NETEASE_COOKIE="$(cat /tmp/ne_cookie.txt)" go test -run TestLyricLive ./pkg/netease/
func TestLyricLive(t *testing.T) {
	cookie := strings.TrimSpace(os.Getenv("NETEASE_COOKIE"))
	if cookie == "" {
		t.Skip("未设置 NETEASE_COOKIE，跳过")
	}
	c := New(cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 2151853884 = 《我会好好的 (Live版)》
	lrc, err := c.Lyric(ctx, 2151853884)
	if err != nil {
		t.Fatalf("lyric: %v", err)
	}
	if strings.TrimSpace(lrc) == "" {
		t.Fatal("歌词为空")
	}
	lines := strings.Split(strings.TrimSpace(lrc), "\n")
	t.Logf("歌词 %d 行，前 3 行：%v", len(lines), lines[:min(3, len(lines))])
	if out := os.Getenv("NETEASE_LYRIC_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(lrc), 0o644); err != nil {
			t.Fatalf("写歌词文件: %v", err)
		}
		t.Logf("已写出 %s", out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
