package main

import "testing"

// TestDecideMusic 对应 spec voice-routing 的全部场景：音乐态判定树是纯函数，
// 不需要模型、不需要设备就能验证。
func TestDecideMusic(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		in         musicInput
		wantPath   musicPath
		wantAction string
		wantVia    string
	}{
		{
			name: "不在放音乐时不接管", text: "豆包豆包",
			in:       musicInput{Matched: true, WakeRest: ""},
			wantPath: musicPathNotMusic,
		},
		{
			name: "裸指令 停下", text: "停下",
			in:       musicInput{Playing: true},
			wantPath: musicPathWhitelist, wantAction: "stop",
		},
		{
			name: "裸指令 安静", text: "安静",
			in:       musicInput{Playing: true},
			wantPath: musicPathWhitelist, wantAction: "mute",
		},
		{
			name: "裸指令 下一首", text: "下一首",
			in:       musicInput{Playing: true},
			wantPath: musicPathWhitelist, wantAction: "next",
		},
		{
			name: "唤醒词加裸指令", text: "豆包豆包下一首",
			in:       musicInput{Matched: true, WakeRest: "下一首", Playing: true},
			wantPath: musicPathWhitelist, wantAction: "next",
		},
		{
			name: "唤醒词加复杂请求", text: "豆包豆包换点安静的歌",
			in:       musicInput{Matched: true, WakeRest: "换点安静的歌", Playing: true},
			wantPath: musicPathRoute, wantVia: "wake",
		},
		{
			name: "只喊了唤醒词", text: "豆包豆包",
			in:       musicInput{Matched: true, WakeRest: "", Playing: true},
			wantPath: musicPathBareWake,
		},
		{
			name: "无唤醒词但带动作词", text: "帮换一首如愿",
			in:       musicInput{Playing: true},
			wantPath: musicPathRoute, wantVia: "verb",
		},
		{
			name: "带上下文不命中白名单", text: "这首歌太吵了",
			in:       musicInput{Playing: true},
			wantPath: musicPathDrop,
		},
		{
			name: "歌词碎片", text: "也睡不在。",
			in:       musicInput{Playing: true},
			wantPath: musicPathDrop,
		},
		{
			name: "安静的歌不是静音", text: "安静的歌",
			in:       musicInput{Playing: true},
			wantPath: musicPathDrop,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideMusic(c.text, c.in)
			if got.Path != c.wantPath {
				t.Fatalf("path = %v, want %v (decision=%+v)", got.Path, c.wantPath, got)
			}
			if c.wantAction != "" && got.Action != c.wantAction {
				t.Fatalf("action = %q, want %q", got.Action, c.wantAction)
			}
			if c.wantVia != "" && got.Via != c.wantVia {
				t.Fatalf("via = %q, want %q", got.Via, c.wantVia)
			}
		})
	}
}

// TestPhaseEquivalence 阶段枚举与旧的布尔查询必须等价（迁移期防止语义漂移）。
func TestPhaseEquivalence(t *testing.T) {
	s := NewSpeaker()

	if s.Phase() != PhaseIdle || s.Speaking() || s.MusicPlaying() {
		t.Fatal("新建 Speaker 应该是 idle")
	}

	s.markSpeaking(true)
	if s.Phase() != PhaseSpeaking || !s.Speaking() {
		t.Fatalf("播报中阶段 = %v", s.Phase())
	}

	// 播完仍有 700ms 冷却，阶段还是 speaking
	s.markSpeaking(false)
	if !s.Speaking() || s.Phase() != PhaseSpeaking {
		t.Fatal("冷却期内仍应视为播报中")
	}

	// 放音乐（直接置状态，不走设备）
	s = NewSpeaker()
	s.stMu.Lock()
	s.st.music = true
	s.stMu.Unlock()
	if !s.MusicPlaying() || s.Phase() != PhaseMusic {
		t.Fatalf("音乐态阶段 = %v", s.Phase())
	}
}

// TestImmediatePlayWindow 路由期间"点歌立刻起播"的窗口是 Speaker 拥有的状态。
func TestImmediatePlayWindow(t *testing.T) {
	s := NewSpeaker()
	if s.ImmediatePlayActive() {
		t.Fatal("默认不应该在立即起播窗口")
	}
	s.BeginImmediatePlay()
	if !s.ImmediatePlayActive() {
		t.Fatal("Begin 后应该在窗口内")
	}
	s.EndImmediatePlay()
	if s.ImmediatePlayActive() {
		t.Fatal("End 后应该退出窗口")
	}
}
