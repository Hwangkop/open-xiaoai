package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
)

func TestNativeReplyWithMusicDoesNotReopenMicrophone(t *testing.T) {
	s := NewSpeaker()
	s.st.music = true
	e := &Engine{speaker: s}
	e.turnSeq.Store(7)
	e.nativeChain.Store(3)
	rpc := connect.GetRPC()
	var calls []string
	rpc.Init(func(req connect.Request) error {
		calls = append(calls, req.Command)
		resp := connect.SuccessResponse()
		resp.ID = req.ID
		rpc.OnResponse(resp)
		return nil
	})
	t.Cleanup(rpc.Dispose)

	e.continueNativeAfterReply(context.Background(), 7)
	if len(calls) != 0 || s.NativeASRWindow() {
		t.Fatalf("音乐态回答结束不应再开麦：calls=%v window=%v", calls, s.NativeASRWindow())
	}
	if !s.MusicPlaying() || e.nativeChain.Load() != 0 {
		t.Fatal("应保持音乐态并结束自动续听链")
	}
}

func TestNativeContinuationOwnsWindowAndEmptyFinalClosesIt(t *testing.T) {
	s := NewSpeaker()
	e := &Engine{speaker: s}
	e.turnSeq.Store(1)
	rpc := connect.GetRPC()
	calls := 0
	rpc.Init(func(req connect.Request) error {
		if req.Command == "native_continue_dialog" && !s.NativeASRWindow() {
			t.Fatalf("开麦前应已持有窗口：command=%s window=%v", req.Command, s.NativeASRWindow())
		}
		if req.Command == "native_continue_dialog" {
			calls++
		}
		resp := connect.SuccessResponse()
		resp.ID = req.ID
		rpc.OnResponse(resp)
		return nil
	})
	t.Cleanup(rpc.Dispose)
	e.continueNativeAfterReply(context.Background(), 1)
	if calls != 1 || !s.NativeASRWindow() {
		t.Fatal("非音乐态应续听一次并保留窗口")
	}

	emit := func(ns, name, dialog string, final bool) {
		t.Helper()
		line, _ := json.Marshal(map[string]any{
			"header":  map[string]string{"namespace": ns, "name": name, "dialog_id": dialog},
			"payload": map[string]any{"is_final": final, "results": []map[string]string{{"text": ""}}},
		})
		data, _ := json.Marshal(map[string]string{"NewLine": string(line)})
		e.handleInstruction(data)
	}
	// 上一轮迟到的 Finish 不能关闭新开的窗口。
	emit("Dialog", "Finish", "old", false)
	if !s.NativeASRWindow() {
		t.Fatal("旧 Finish 不应关闭新窗口")
	}
	emit("SpeechRecognizer", "RecognizeResult", "new", false)
	emit("SpeechRecognizer", "RecognizeResult", "old", true)
	if !s.NativeASRWindow() {
		t.Fatal("旧空 final 不应关闭新窗口")
	}
	emit("SpeechRecognizer", "RecognizeResult", "new", true)
	if s.NativeASRWindow() {
		t.Fatal("本轮空 final 应关闭窗口")
	}
}

func TestNativeASRWindowOldTimeoutDoesNotCloseNewWindow(t *testing.T) {
	s := NewSpeaker()
	old := s.BeginNativeASRWindow()
	current := s.BeginNativeASRWindow()
	s.EndNativeASRWindowFor(old)
	if !s.NativeASRWindow() {
		t.Fatal("旧超时不应关闭新窗口")
	}
	s.EndNativeASRWindowFor(current)
	if s.NativeASRWindow() {
		t.Fatal("当前窗口应能正常关闭")
	}
}

func TestStaleOrCancelledReplyDoesNotContinueNativeDialog(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		s := NewSpeaker()
		e := &Engine{speaker: s}
		e.turnSeq.Store(2)
		ctx, cancel := context.WithCancel(context.Background())
		turn := uint64(1)
		if cancelled {
			turn = 2
			cancel()
		}
		e.continueNativeAfterReply(ctx, turn)
		cancel()
		if s.NativeASRWindow() || e.nativeChain.Load() != 0 {
			t.Fatal("旧轮次或已取消轮次不应续听")
		}
	}
}

// 指令隔离不能退回到播放后的 stop 竞态，也不能误发冻结/解冻。
func TestInstructionTakeoverNeverSignalsOrStopsNativePlayer(t *testing.T) {
	s := NewSpeaker()
	s.nativeInstructionIsolation.Store(true)
	s.nativeFreeze = func() (int, error) { t.Fatal("指令隔离不应冻结"); return 0, nil }
	s.nativeUnfreeze = func(int) error { t.Fatal("指令隔离不应解冻"); return nil }
	s.nativeStopTTS = func(string) { t.Error("指令隔离不应停止 mibrain") }
	if err := s.BeginNativeTakeover(time.Second, true); err != nil {
		t.Fatal(err)
	}
	s.StopNativeTTS("开始处理新消息")
	s.EndNativeTakeover("测试")
	time.Sleep(180 * time.Millisecond)
	if s.TakeoverActive() || s.LateNativeTTSSuppressed() {
		t.Fatal("指令隔离收尾不能残留接管/清队列定时任务")
	}
}

func TestTurnKeyNormalizesFullwidthAndWhitespace(t *testing.T) {
	for _, text := range []string{"小爱同学，现在几点？", "小 爱 同 学　现 在 几 点", "现在几点。"} {
		if got := turnKey(text); got != "现在几点" {
			t.Errorf("%q -> %q", text, got)
		}
	}
	if turnKey("ＡＢＣ１２３") != turnKey("abc123") {
		t.Fatal("全角 ASCII 应与半角一致")
	}
	e := &Engine{}
	if !e.claimTurn("小爱同学，现在几点？") || e.claimTurn("现在 几点") {
		t.Fatal("两路 ASR 的同一句只能认领一次")
	}
}
