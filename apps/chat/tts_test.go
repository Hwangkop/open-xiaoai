package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDoubaoTTS_MP3 会真实调用豆包语音合成接口（需要网络），-short 模式下跳过。
func TestDoubaoTTS_MP3(t *testing.T) {
	if testing.Short() {
		t.Skip("skip network test in -short mode")
	}

	tts := NewDoubaoTTS(TTSConfig{Provider: "doubao", Format: "mp3"})
	if tts.Dir() != defaultTTSDir() {
		t.Fatalf("unexpected cache dir: %s", tts.Dir())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	name, err := tts.Synthesize(ctx, "你好，我是豆包。")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if filepath.Ext(name) != ".mp3" {
		t.Fatalf("unexpected file name: %s", name)
	}

	data, err := os.ReadFile(filepath.Join(tts.Dir(), name))
	if err != nil {
		t.Fatalf("read audio: %v", err)
	}
	if len(data) < 1000 {
		t.Fatalf("audio too small: %d bytes", len(data))
	}
	// MP3 应带 ID3 头或帧同步 0xFFEx
	if string(data[:3]) != "ID3" && !(data[0] == 0xFF && data[1]&0xE0 == 0xE0) {
		t.Fatalf("not an mp3, head=% x", data[:8])
	}
	t.Logf("synthesized %d bytes -> %s", len(data), name)

	// 第二次应命中缓存，不再发请求
	start := time.Now()
	name2, err := tts.Synthesize(ctx, "你好，我是豆包。")
	if err != nil {
		t.Fatalf("synthesize(cached): %v", err)
	}
	if name2 != name {
		t.Fatalf("cache name mismatch: %s vs %s", name, name2)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Logf("cached call took %v (expected fast)", d)
	}
}

func TestDecodeVoiceFrame(t *testing.T) {
	// 入站帧格式：f4=event, f7=JSON 元信息, f8=音频分片
	var in []byte
	in = appendVoiceField(in, 4, "TTSResponse")
	in = appendVoiceField(in, 7, `{"timestamp_ms":1}`)
	in = appendVoiceField(in, 8, "audio-bytes")

	event, payload, audio := decodeVoiceFrame(in)
	if event != "TTSResponse" {
		t.Fatalf("event mismatch: %q", event)
	}
	if payload != `{"timestamp_ms":1}` {
		t.Fatalf("payload mismatch: %q", payload)
	}
	if string(audio) != "audio-bytes" {
		t.Fatalf("audio mismatch: %q", string(audio))
	}

	// 解码器只用于入站帧；出站帧复用同号字段，这里仅确保不会 panic。
	_, _, _ = decodeVoiceFrame(encodeVoiceFrame("StartTask", "{}", "sid-1"))
}

func TestTTSSessionPayloadPostProcess(t *testing.T) {
	base := NewDoubaoTTS(TTSConfig{Provider: "doubao"})
	if payload := base.sessionPayload(); strings.Contains(payload, "post_process") {
		t.Fatalf("default speed/pitch should not send post_process: %s", payload)
	}

	fast := NewDoubaoTTS(TTSConfig{Provider: "doubao", SpeechRate: 1.5, Pitch: 0.2})
	payload := fast.sessionPayload()
	if !strings.Contains(payload, `"speech_rate":1.5`) {
		t.Fatalf("expected speech_rate in payload: %s", payload)
	}
	if !strings.Contains(payload, `"pitch":0.2`) {
		t.Fatalf("expected pitch in payload: %s", payload)
	}

	// 语速/音调不同的文本不应共用缓存
	if base.cacheName("测试") == fast.cacheName("测试") {
		t.Fatal("cache name should include speech rate and pitch")
	}
}

func TestTTSConfigUsesDoubao(t *testing.T) {
	if !(TTSConfig{Provider: "Doubao"}).UsesDoubao() {
		t.Fatal("expected provider doubao to be detected")
	}
	if (TTSConfig{Provider: "xiaoai"}).UsesDoubao() {
		t.Fatal("xiaoai should not use doubao")
	}
	if (TTSConfig{}).UsesDoubao() {
		t.Fatal("empty provider should not use doubao")
	}
}

// TestTTSSpeakerWiring 校验 provider 切换与音色 URL 推导。
func TestTTSSpeakerWiring(t *testing.T) {
	cfg := defaultConfig()
	cfg.TTS = TTSConfig{Provider: "doubao", Format: "mp3"}
	speaker := NewSpeaker()
	app := newAppRuntime("config.yaml", cfg, speaker, NewEngine(cfg, speaker))

	if got := app.ttsBaseURL(); got != "" {
		t.Fatalf("expected empty base url before any connection, got %q", got)
	}

	app.OnConnectionHost("192.168.1.50")
	if got := app.ttsBaseURL(); got != "http://192.168.1.50:4399" {
		t.Fatalf("unexpected base url: %q", got)
	}

	app.ApplyTTS(cfg)
	if speaker.doubaoTTSInstance() == nil {
		t.Fatal("expected doubao tts enabled")
	}

	app.ApplyTTS(&AppConfig{TTS: TTSConfig{Provider: "xiaoai"}})
	if speaker.doubaoTTSInstance() != nil {
		t.Fatal("expected doubao tts disabled for xiaoai provider")
	}
}
