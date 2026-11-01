package main

// 用真实歌词校准歌词匹配阈值（需要 NETEASE_COOKIE）。
// 跑法：NETEASE_COOKIE="$(cat /tmp/ne_cookie.txt)" go test -run TestLyricExplainLive -v

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

func TestLyricExplainLive(t *testing.T) {
	cookie := os.Getenv("NETEASE_COOKIE")
	if cookie == "" {
		t.Skip("未设置 NETEASE_COOKIE")
	}
	c := netease.New(cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	songs, err := c.Search(ctx, "龙愿 廖昌永", 5)
	if err != nil || len(songs) == 0 {
		t.Fatalf("search: %v", err)
	}
	song := songs[0]
	lrc, err := c.Lyric(ctx, song.ID)
	if err != nil {
		t.Fatalf("lyric: %v", err)
	}
	lines := parseLRC(lrc)
	t.Logf("《%s》- %s，歌词 %d 行", song.Name, song.Artists, len(lines))

	for _, text := range []string{"要眼记。", "也睡不在。", "我香动物。", "真我一个。", "一晚或是。", "我想听龙愿。"} {
		hit := lyricExplain(lines, -1, text) // 位置未知=全量比对（最宽松）
		t.Logf("%-10q dist=%d limit=%d matched=%v  最近: %q", text, hit.Distance, hit.Limit, hit.Matched, hit.Line)
		for _, c := range hit.Candidates {
			t.Logf("      cand dist=%d %q", c.Distance, c.Line)
		}
	}
}
