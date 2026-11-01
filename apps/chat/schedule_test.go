package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanForSpeech(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "去掉加粗和标题",
			in:   "## 今日提醒\n\n**到点啦**，喝杯水～",
			want: "今日提醒\n\n到点啦，喝杯水～",
		},
		{
			name: "去掉列表标记",
			in:   "- 第一项\n- 第二项\n1. 第三项",
			want: "第一项\n第二项\n第三项",
		},
		{
			name: "链接保留文字去掉地址",
			in:   "详见[官方文档](https://example.com/a?b=1)，或访问 https://example.com/x",
			want: "详见官方文档，或访问",
		},
		{
			name: "去掉 emoji",
			in:   "到点啦，喝杯水润润嗓子～💧",
			want: "到点啦，喝杯水润润嗓子～",
		},
		{
			name: "整段丢弃代码块",
			in:   "说明如下：\n```go\nfmt.Println(1)\n```\n结束",
			want: "说明如下：\n结束",
		},
		{
			name: "表格竖线换成空格",
			in:   "| 项目 | 值 |\n|------|-----|\n| 温度 | 26 |",
			want: "项目 值\n\n温度 26",
		},
		{
			name: "引用块去掉符号",
			in:   "> 引用的一句话",
			want: "引用的一句话",
		},
		{
			name: "空输入",
			in:   "   \n\t ",
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CleanForSpeech(c.in)
			if got != c.want {
				t.Fatalf("CleanForSpeech(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

func TestCleanForSpeechKeepsPlainText(t *testing.T) {
	in := "到点啦，喝杯水润润嗓子，休息一下再继续～"
	if got := CleanForSpeech(in); got != in {
		t.Fatalf("纯文本不应被改动，got %q", got)
	}
}

func TestParseQuietHours(t *testing.T) {
	cases := []struct {
		spec       string
		start, end int
		ok         bool
	}{
		{"23:00-07:00", 23 * 60, 7 * 60, true},
		{"09:30-18:00", 9*60 + 30, 18 * 60, true},
		{"", 0, 0, false},
		{"25:00-07:00", 0, 0, false},
		{"0900-1800", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := parseQuietHours(c.spec)
		if ok != c.ok || (ok && (s != c.start || e != c.end)) {
			t.Fatalf("parseQuietHours(%q) = (%d,%d,%v), want (%d,%d,%v)", c.spec, s, e, ok, c.start, c.end, c.ok)
		}
	}
}

func TestIsQuietCrossMidnight(t *testing.T) {
	cfg := ScheduleConfig{QuietHours: "23:00-07:00"}
	cases := []struct {
		hour, minute int
		want         bool
	}{
		{23, 0, true},
		{23, 59, true},
		{0, 30, true},
		{6, 59, true},
		{7, 0, false},
		{12, 0, false},
		{22, 59, false},
	}
	for _, c := range cases {
		now := time.Date(2026, 10, 30, c.hour, c.minute, 0, 0, time.UTC)
		if got := cfg.isQuiet(now, nil); got != c.want {
			t.Fatalf("%02d:%02d isQuiet=%v, want %v", c.hour, c.minute, got, c.want)
		}
	}
}

func TestIsQuietSameDayRange(t *testing.T) {
	cfg := ScheduleConfig{QuietHours: "09:00-18:00"}
	at := func(h, m int) bool {
		return cfg.isQuiet(time.Date(2026, 10, 30, h, m, 0, 0, time.UTC), nil)
	}
	if at(8, 59) || !at(9, 0) || !at(17, 59) || at(18, 0) {
		t.Fatal("同日静默区间判断错误")
	}
}

func TestIsQuietEmptyMeansNever(t *testing.T) {
	cfg := ScheduleConfig{}
	if cfg.isQuiet(time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), nil) {
		t.Fatal("未配置静默时段时不应静默")
	}
}

func TestScheduleStatePersistAndPrune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	st := loadScheduleState(path)
	st.Put("job-a", jobRecord{LastRunTime: "100", MessageID: "m1", LastText: "喝水"})
	st.Put("job-b", jobRecord{LastRunTime: "200", MessageID: "m2"})

	// 重新加载应保留
	reloaded := loadScheduleState(path)
	if rec, ok := reloaded.Get("job-a"); !ok || rec.MessageID != "m1" || rec.LastText != "喝水" {
		t.Fatalf("状态未正确持久化: %+v", rec)
	}

	// 只保留 job-b
	reloaded.prune(map[string]bool{"job-b": true})
	again := loadScheduleState(path)
	if _, ok := again.Get("job-a"); ok {
		t.Fatal("被删除的任务应从状态文件里清理掉")
	}
	if _, ok := again.Get("job-b"); !ok {
		t.Fatal("活跃任务不应被清理")
	}
}

func TestScheduleStateMissingFile(t *testing.T) {
	st := loadScheduleState(filepath.Join(t.TempDir(), "nope.json"))
	if len(st.Snapshot()) != 0 {
		t.Fatal("缺失状态文件时应从空白开始")
	}
}

func TestScheduleStateCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := loadScheduleState(path)
	if len(st.Snapshot()) != 0 {
		t.Fatal("损坏的状态文件应被忽略")
	}
	// 仍应可写
	st.Put("x", jobRecord{LastRunTime: "1"})
	if _, ok := loadScheduleState(path).Get("x"); !ok {
		t.Fatal("损坏文件应能被覆盖写入")
	}
}

func TestWatcherInterval(t *testing.T) {
	cases := []struct {
		spec string
		want time.Duration
	}{
		{"60s", 60 * time.Second},
		{"30s", 30 * time.Second},
		{"5s", 60 * time.Second},   // 低于下限回落默认
		{"", 60 * time.Second},     // 空值
		{"abc", 60 * time.Second},  // 非法值
	}
	for _, c := range cases {
		w := &ScheduleWatcher{cfg: ScheduleConfig{PollInterval: c.spec}}
		if got := w.Interval(); got != c.want {
			t.Fatalf("Interval(%q)=%v, want %v", c.spec, got, c.want)
		}
	}
}

func TestJobListJSUsesNumericConversationID(t *testing.T) {
	js := doubaoLatestMessageJS("123456789")
	if !strings.Contains(js, "conversation_id: '123456789'") {
		t.Fatalf("会话 ID 未正确内联: %s", js)
	}
	if strings.Contains(js, "%!") {
		t.Fatalf("脚本含格式化错误: %s", js)
	}
	if !strings.Contains(js, "block_type === 10000") {
		t.Fatal("应只取纯文本块")
	}
}
