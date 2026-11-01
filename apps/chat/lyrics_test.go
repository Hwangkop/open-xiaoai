package main

import (
	"testing"
	"time"
)

func TestParseLRC(t *testing.T) {
	lrc := `[00:01.20]作词 : 张三
[00:12.34]第一句歌词
[00:15.5]第二句
[01:02.123][01:30.00]重复的一句
`
	lines := parseLRC(lrc)
	if len(lines) != 5 {
		t.Fatalf("len = %d, want 5（一行多时间戳要展开）: %+v", len(lines), lines)
	}
	if lines[0].At != 1200*time.Millisecond || lines[0].Text != "作词 : 张三" {
		t.Fatalf("line0 = %+v", lines[0])
	}
	if lines[1].At != 12340*time.Millisecond || lines[1].Text != "第一句歌词" {
		t.Fatalf("line1 = %+v", lines[1])
	}
	if lines[2].At != 15500*time.Millisecond {
		t.Fatalf("一位小数是十分之一秒: %+v", lines[2])
	}
	// 一行多个时间戳要展开成多行
	if lines[3].At != 62123*time.Millisecond || lines[3].Text != "重复的一句" {
		t.Fatalf("line3 = %+v", lines[3])
	}
	if lines[4].At != 90*time.Second {
		t.Fatalf("line4 = %+v", lines[4])
	}
}

func TestLyricMatches(t *testing.T) {
	lines := []lyricLine{
		{At: 10 * time.Second, Text: "我会好好的"},
		{At: 20 * time.Second, Text: "留下来陪我过完这个冬天"},
		{At: 60 * time.Second, Text: "别走"},
	}
	cases := []struct {
		pos  time.Duration
		text string
		want bool
	}{
		{12 * time.Second, "我会好好得", true},    // 同音字
		{21 * time.Second, "留下来陪我", true},    // 片段命中
		{12 * time.Second, "下一首", false},     // 短句不放宽
		{45 * time.Second, "留下来陪我", false},   // 超出时间窗（±20s 之外）
		{61 * time.Second, "别走", false},      // 只有 2 个音节，不判
		{12 * time.Second, "今天天气不错啊", false}, // 不像歌词
	}
	for _, c := range cases {
		if got := lyricMatches(lines, c.pos, c.text); got != c.want {
			t.Errorf("lyricMatches(%q @%s) = %v, want %v", c.text, c.pos, got, c.want)
		}
	}
}
