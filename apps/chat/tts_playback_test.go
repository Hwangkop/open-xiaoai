package main

import (
	"path/filepath"
	"testing"
)

func TestTTSVolumeRaw(t *testing.T) {
	cases := []struct {
		percent int
		want    int
	}{
		{-1, 0},
		{0, 0},
		{60, 153},
		{100, 255},
		{101, 255},
	}
	for _, c := range cases {
		if got := ttsVolumeRaw(c.percent); got != c.want {
			t.Fatalf("ttsVolumeRaw(%d) = %d, want %d", c.percent, got, c.want)
		}
	}
}

func TestTTSVolumeRecord(t *testing.T) {
	old := ttsVolumeFile
	defer func() { ttsVolumeFile = old }()
	ttsVolumeFile = filepath.Join(t.TempDir(), "tts-volume.json")

	if _, ok := loadTTSVolumeRecord(); ok {
		t.Fatal("expected no record before save")
	}
	saveTTSVolumeRecord(ttsVolumeRecord{Volume: 55})
	rec, ok := loadTTSVolumeRecord()
	if !ok || rec.Volume != 55 {
		t.Fatalf("load record = %+v, ok=%v; want volume 55", rec, ok)
	}
	ClearTTSVolumeFile()
	if _, ok := loadTTSVolumeRecord(); ok {
		t.Fatal("expected record cleared")
	}

	// 越界值不采纳
	saveTTSVolumeRecord(ttsVolumeRecord{Volume: 150})
	if _, ok := loadTTSVolumeRecord(); ok {
		t.Fatal("expected out-of-range record to be ignored")
	}
}

func TestTTSPlaybackSettings(t *testing.T) {
	s := NewSpeaker()

	// auto + wav -> aplay + notify，音量 60% 映射到 153
	s.SetTTSPlayback("auto", 60, "notify")
	player, device, raw := s.ttsPlaybackSettings("http://server/tts/a.wav")
	if player != "aplay" || device != "notify" || raw != 153 {
		t.Fatalf("auto wav: got player=%q device=%q raw=%d", player, device, raw)
	}

	// auto + mp3 -> miplayer
	player, _, _ = s.ttsPlaybackSettings("http://server/tts/a.mp3")
	if player != "miplayer" {
		t.Fatalf("auto mp3: got player=%q", player)
	}

	// 显式 aplay 但音频是 mp3 -> 回退 miplayer
	s.SetTTSPlayback("aplay", 60, "notify")
	player, _, _ = s.ttsPlaybackSettings("http://server/tts/a.mp3")
	if player != "miplayer" {
		t.Fatalf("aplay with mp3: got player=%q", player)
	}

	// 显式 miplayer 但音频是 wav -> 回退 aplay
	s.SetTTSPlayback("miplayer", 60, "notify")
	player, _, _ = s.ttsPlaybackSettings("http://server/tts/a.wav")
	if player != "aplay" {
		t.Fatalf("miplayer with wav: got player=%q", player)
	}

	// 未知 wav_device 归一到 notify；playback 保留
	s.SetTTSPlayback("auto", 60, "playback")
	_, device, _ = s.ttsPlaybackSettings("http://server/tts/a.wav")
	if device != "playback" {
		t.Fatalf("playback device: got %q", device)
	}
	s.SetTTSPlayback("auto", 60, "something")
	_, device, _ = s.ttsPlaybackSettings("http://server/tts/a.wav")
	if device != "notify" {
		t.Fatalf("unknown device should normalize to notify, got %q", device)
	}
}
