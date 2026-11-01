package main

import (
	"context"
	"testing"
)

func newRouterTestEngine(t *testing.T, minConf float64) (*Engine, *Speaker) {
	t.Helper()
	cfg := &AppConfig{
		LLM:         LLMConfig{BaseURL: "http://127.0.0.1:1", APIKey: "x", Model: "test"},
		MusicRouter: MusicRouterConfig{Enabled: true, TimeoutMs: 100, MinConfidence: minConf},
	}
	sp := NewSpeaker()
	vol := 50
	sp.volumeGet = func() (int, error) { return vol, nil }
	sp.volumeSet = func(v int) error { vol = v; return nil }
	return NewEngine(cfg, sp), sp
}

// TestApplyMusicDecisionLowConfidenceIgnores 置信度不够不能动手。
func TestApplyMusicDecisionLowConfidenceIgnores(t *testing.T) {
	e, _ := newRouterTestEngine(t, 0.7)
	called := false
	e.SetMusicControls(
		func(context.Context) (string, error) { called = true; return "", nil },
		func() { called = true },
	)
	e.applyMusicDecision(context.Background(), "下一首", musicDecision{Intent: "next", Confidence: 0.4}, 10, "wake")
	if called {
		t.Fatal("低置信度不该执行动作")
	}
}

// TestApplyMusicDecisionDispatches next/stop/volume 要走到对应实现。
func TestApplyMusicDecisionDispatches(t *testing.T) {
	e, sp := newRouterTestEngine(t, 0.6)
	nextCalled, stopCalled := false, false
	e.SetMusicControls(
		func(context.Context) (string, error) { nextCalled = true; return "ok", nil },
		func() { stopCalled = true },
	)

	e.applyMusicDecision(context.Background(), "换一首", musicDecision{Intent: "next", Confidence: 0.9}, 10, "wake")
	if !nextCalled {
		t.Fatal("next 意图应调用下一首")
	}

	e.applyMusicDecision(context.Background(), "关掉音乐", musicDecision{Intent: "stop", Confidence: 0.9}, 10, "wake")
	if !stopCalled {
		t.Fatal("stop 意图应停止播放")
	}

	// volume_up：非 duck 状态下直接读/写设备音量
	before, err := sp.volume()
	if err != nil {
		t.Fatalf("volume: %v", err)
	}
	e.applyMusicDecision(context.Background(), "大点声", musicDecision{Intent: "volume_up", Confidence: 0.9}, 10, "wake")
	after, _ := sp.volume()
	if after != before+musicVolumeStep {
		t.Fatalf("volume %d -> %d, want +%d", before, after, musicVolumeStep)
	}
}

// TestApplyMusicDecisionIgnoresUnknown 未知 intent 或 ignore 都不能 panic。
func TestApplyMusicDecisionIgnoresUnknown(t *testing.T) {
	e, _ := newRouterTestEngine(t, 0.6)
	for _, in := range []string{"ignore", "", "whatever", "NEXT"} {
		e.applyMusicDecision(context.Background(), "嗯", musicDecision{Intent: in, Confidence: 0.99}, 10, "wake")
	}
}

// TestRouteLogRecordsDecision 路由结果要进观测缓冲（管理台「音乐态路由」卡片的数据源）。
func TestRouteLogRecordsDecision(t *testing.T) {
	clearRouteLog()
	e, _ := newRouterTestEngine(t, 0.6)
	e.SetMusicControls(
		func(context.Context) (string, error) { return "ok", nil },
		func() {},
	)
	e.applyMusicDecision(context.Background(), "换一首", musicDecision{Intent: "next", Confidence: 0.93}, 42, "wake")

	items, stats := routeLogSnapshot()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	got := items[0]
	if got.Stage != "router" || got.Action != "next" || got.Result != "executed" || got.LatencyMs != 42 {
		t.Fatalf("entry = %+v", got)
	}
	if stats["total"].(int) != 1 {
		t.Fatalf("total = %v", stats["total"])
	}
	if m := stats["intent"].(map[string]int); m["next"] != 1 {
		t.Fatalf("intent counter = %v", m)
	}

	// 低置信度要记成 low_confidence，而不是不留痕
	e.applyMusicDecision(context.Background(), "嗯", musicDecision{Intent: "play", Confidence: 0.2, Query: "x"}, 10, "wake")
	items, _ = routeLogSnapshot()
	if items[len(items)-1].Result != "low_confidence" {
		t.Fatalf("last = %+v", items[len(items)-1])
	}
	clearRouteLog()
}
