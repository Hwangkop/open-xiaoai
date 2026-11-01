package main

import (
	"context"
	"testing"
)

// TestMusicVolumeIntent 覆盖字面、同音字、以及不该命中的情况。
func TestMusicVolumeIntent(t *testing.T) {
	cases := []struct {
		text string
		want musicVolumeAction
	}{
		{"大点声", musicVolumeUp},
		{"大点声。", musicVolumeUp},
		{"大电声", musicVolumeUp}, // 同音字，拼音完全一致
		{"声音大一点", musicVolumeUp},
		{"太小声了", musicVolumeUp},
		{"小点声", musicVolumeDown},
		{"声音小一点", musicVolumeDown},
		{"太吵了", musicVolumeDown},
		{"轻一点", musicVolumeDown},
		{"小一点", musicVolumeDown}, // 不能因为"大一点"的拼音距离被判成调大
		{"静音", musicMuteOn},
		{"安静点", musicMuteOn},
		{"取消静音", musicMuteOff}, // 含"静音"二字，必须先判取消
		{"恢复音量", musicMuteOff},
		{"下一首", musicVolumeNone},
		{"唱一首", musicVolumeNone},
		{"今天天气不错", musicVolumeNone},
		{"", musicVolumeNone},
		{"。", musicVolumeNone},
	}
	for _, c := range cases {
		if got := musicVolumeIntent(c.text); got != c.want {
			t.Errorf("musicVolumeIntent(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestMusicVolumeIntentKeepsOtherMusicCommands 音量判定不能抢走已有的音乐指令。
func TestMusicVolumeIntentKeepsOtherMusicCommands(t *testing.T) {
	for _, text := range []string{"下一首", "换一首", "切歌", "停止", "暂停", "关掉音乐"} {
		if got := musicVolumeIntent(text); got != musicVolumeNone {
			t.Errorf("%q 不该被当成音量指令，got %v", text, got)
		}
	}
}

// TestIsBareCommand 只有"基本就是这条指令"才算白名单命中；
// 「帮换一首如愿」这种带上下文的要留给路由器（用户要的是《如愿》）。
func TestIsBareCommand(t *testing.T) {
	cases := []struct {
		text, keyword string
		want          bool
	}{
		{"换一首", "换一首", true},
		{"换一首吧", "换一首", true},
		{"帮我暂停一下", "暂停", true},
		{"停止播放", "停止", true},
		{"太吵了", "太吵", true},
		{"帮换一首如愿", "换一首", false},
		{"换一首周杰伦的", "换一首", false},
		{"播放周杰伦", "停止", false},
	}
	for _, c := range cases {
		if got := isBareCommand(c.text, c.keyword); got != c.want {
			t.Errorf("isBareCommand(%q, %q) = %v, want %v", c.text, c.keyword, got, c.want)
		}
	}
}

// TestMusicVolumeIntentBareOnly 只有"基本就是这句指令"的说法才动作；
// 歌词里带这些词的片段不能触发（房间里的歌词就是靠这条挡住的）。
func TestMusicVolumeIntentBareOnly(t *testing.T) {
	cases := []struct {
		text string
		want musicVolumeAction
	}{
		{"安静", musicMuteOn},
		{"安静点", musicMuteOn},
		{"安静的歌", musicVolumeNone}, // 是"换安静的歌"，不是静音
		{"大点声", musicVolumeUp},
		{"太吵了", musicVolumeDown},
		{"这首太吵了", musicVolumeNone}, // 带上下文，交给路由/主 agent
	}
	for _, c := range cases {
		if got := musicVolumeIntent(c.text); got != c.want {
			t.Errorf("musicVolumeIntent(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestHandleMusicVoiceStopVariants 音乐态第一道门：不带唤醒词的停止/切歌必须识别。
func TestHandleMusicVoiceStopVariants(t *testing.T) {
	cfg := &AppConfig{LLM: LLMConfig{BaseURL: "http://127.0.0.1:1", APIKey: "x", Model: "t"}}
	e := NewEngine(cfg, NewSpeaker())
	stopped := false
	e.SetMusicControls(
		func(context.Context) (string, error) { return "ok", nil },
		func() { stopped = true },
	)
	for _, w := range []string{"停下", "停", "停止", "安静", "暂停", "关掉音乐"} {
		stopped = false
		action, ok := e.handleMusicVoice(w)
		if !ok {
			t.Errorf("%q 应该被第一道门接住", w)
			continue
		}
		t.Logf("%q -> %s (stopped=%v)", w, action, stopped)
	}
	if _, ok := e.handleMusicVoice("下一首"); !ok {
		t.Error("下一首 应该被接住")
	}
	for _, w := range []string{"停在那里的风景", "安静的歌", "我要安静一下"} {
		if _, ok := e.handleMusicVoice(w); ok {
			t.Errorf("%q 不该被当成裸指令", w)
		}
	}
}
