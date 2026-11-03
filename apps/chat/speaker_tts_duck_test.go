package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
	"github.com/cxjava/open-xiaoai/apps/client/utils"
)

func newTTSDuckTestSpeaker(t *testing.T, volume int) (*Speaker, *int, *[]int) {
	t.Helper()
	resetMusicDuck(t)
	s := NewSpeaker()
	s.st.music = true
	s.SetTTSMusicDuckFile(filepath.Join(t.TempDir(), "tts-music-duck.json"))
	s.SetTTSMusicDuck(TTSMusicDuckConfig{Enabled: true, Percent: 50})
	get, set, writes := fakeVolume(&volume)
	s.volumeGet, s.volumeSet = get, set
	return s, &volume, writes
}

func startDuckPlayback(t *testing.T, s *Speaker, ctx context.Context) func() {
	t.Helper()
	done, err := s.beginTTSDuckPlayback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func TestTTSDuckReplyTimingAndSentences(t *testing.T) {
	s, vol, writes := newTTSDuckTestSpeaker(t, 20)
	ctx, closeReply := s.WithTTSDuckReply(context.Background())
	if *vol != 20 || len(*writes) != 0 {
		t.Fatal("思考/合成阶段不能压低音乐")
	}
	for i := 0; i < 3; i++ {
		done := startDuckPlayback(t, s, ctx)
		if *vol != 10 {
			t.Fatalf("播报时音量=%d", *vol)
		}
		done()
		if *vol != 10 {
			t.Fatal("句间不能提前恢复")
		}
	}
	closeReply()
	closeReply()
	if *vol != 20 || !reflect.DeepEqual(*writes, []int{10, 20}) {
		t.Fatalf("音量=%d，写入=%v", *vol, *writes)
	}
	if len(s.ttsMusicDuck.replies) != 0 {
		t.Fatal("已完成的范围未清理")
	}
}

func TestTTSDuckOverlapAndCanceledPlayback(t *testing.T) {
	s, vol, writes := newTTSDuckTestSpeaker(t, 20)
	parent, cancel := context.WithCancel(context.Background())
	old, closeOld := s.WithTTSDuckReply(parent)
	oldDone := startDuckPlayback(t, s, old)
	cancel()
	closeOld()
	if *vol != 10 {
		t.Fatal("取消不等于播放器已停止，不能提前恢复")
	}
	if _, err := s.beginTTSDuckPlayback(old); !errors.Is(err, context.Canceled) {
		t.Fatal("已取消的合成不能再播放")
	}
	newer, closeNew := s.WithTTSDuckReply(context.Background())
	newDone := startDuckPlayback(t, s, newer)
	oldDone()
	if *vol != 10 {
		t.Fatal("旧轮次不能恢复新轮次的音量")
	}
	newDone()
	closeNew()
	if *vol != 20 || !reflect.DeepEqual(*writes, []int{10, 20}) {
		t.Fatalf("写入=%v", *writes)
	}
}

func TestTTSDuckNoSpeechNoMusicAndQuiet(t *testing.T) {
	for _, tc := range []struct {
		name           string
		volume         int
		music, enabled bool
	}{
		{"无播报", 20, true, true}, {"无音乐", 20, false, true}, {"关闭", 20, true, false},
		{"静音", 0, true, true}, {"极低", 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, vol, writes := newTTSDuckTestSpeaker(t, tc.volume)
			s.st.music = tc.music
			s.SetTTSMusicDuck(TTSMusicDuckConfig{Enabled: tc.enabled, Percent: 50})
			ctx, closeReply := s.WithTTSDuckReply(context.Background())
			if tc.name != "无播报" {
				startDuckPlayback(t, s, ctx)()
			}
			closeReply()
			if *vol != tc.volume || len(*writes) != 0 {
				t.Fatalf("不该改音量: %v", *writes)
			}
		})
	}
}

func TestTTSDuckManualVolumeAndMuteWin(t *testing.T) {
	for _, action := range []string{"relative", "absolute", "mute", "external"} {
		t.Run(action, func(t *testing.T) {
			s, vol, _ := newTTSDuckTestSpeaker(t, 70)
			s.SetMusicDuck(true, 20)
			s.DuckMusicVolume()
			ctx, closeReply := s.WithTTSDuckReply(context.Background())
			startDuckPlayback(t, s, ctx)()
			want := 7
			switch action {
			case "relative":
				before, after, err := s.AdjustMusicVolume(-3)
				if err != nil || before != 10 || after != 7 {
					t.Fatalf("临时音量应是相对调整基准: %d %d %v", before, after, err)
				}
			case "absolute":
				want = 10 // 即便显式设置为压低后的同值，也不能在收尾时覆盖。
				if err := s.SetMusicVolume(want); err != nil {
					t.Fatal(err)
				}
			case "mute":
				want = 0
				if _, _, err := s.SetMusicMuted(true); err != nil {
					t.Fatal(err)
				}
			case "external":
				*vol = want
			}
			startDuckPlayback(t, s, ctx)()
			closeReply()
			if *vol != want {
				t.Fatalf("用户选择被覆盖: %d want %d", *vol, want)
			}
			if action == "mute" {
				if _, _, err := s.SetMusicMuted(false); err != nil {
					t.Fatal(err)
				}
				if *vol != 20 {
					t.Fatalf("取消静音应回到音乐会话音量，got %d", *vol)
				}
			}
		})
	}
}

func TestTTSDuckSeparateFromMusicSession(t *testing.T) {
	s, vol, writes := newTTSDuckTestSpeaker(t, 70)
	s.SetMusicDuck(true, 20)
	s.DuckMusicVolume()
	ctx, closeReply := s.WithTTSDuckReply(context.Background())
	startDuckPlayback(t, s, ctx)()
	closeReply()
	if *vol != 20 {
		t.Fatalf("TTS 后不能恢复成放歌前的 70: %d", *vol)
	}
	s.RestoreMusicVolume()
	if *vol != 70 || !reflect.DeepEqual(*writes, []int{20, 10, 20, 70}) {
		t.Fatalf("分层恢复错误: %v", *writes)
	}
}

func TestTTSDuckMusicEndsOrHotDisabled(t *testing.T) {
	for _, stopMusic := range []bool{false, true} {
		t.Run(map[bool]string{false: "关闭开关", true: "音乐结束"}[stopMusic], func(t *testing.T) {
			s, vol, _ := newTTSDuckTestSpeaker(t, 70)
			s.SetMusicDuck(true, 20)
			s.DuckMusicVolume()
			ctx, closeReply := s.WithTTSDuckReply(context.Background())
			done := startDuckPlayback(t, s, ctx)
			want := 20
			if stopMusic {
				s.st.music = false
				s.RestoreMusicVolume()
				want = 70
			} else {
				s.SetTTSMusicDuck(TTSMusicDuckConfig{})
			}
			if *vol != want {
				t.Fatalf("即时恢复错误=%d", *vol)
			}
			done()
			closeReply()
			if *vol != want {
				t.Fatal("旧回调不该再改音量")
			}
		})
	}
}

func TestTTSDuckReadWriteAndPersistenceFailures(t *testing.T) {
	for _, mode := range []string{"read", "persist", "write-before", "write-after", "manual-write"} {
		t.Run(mode, func(t *testing.T) {
			s, vol, writes := newTTSDuckTestSpeaker(t, 20)
			originalGet, originalSet := s.volumeGet, s.volumeSet
			switch mode {
			case "read":
				s.volumeGet = func() (int, error) { return 0, errors.New("离线") }
			case "persist":
				s.SetTTSMusicDuckFile(filepath.Join(t.TempDir(), "missing", "record.json"))
			case "write-before":
				s.volumeSet = func(int) error { return errors.New("失败") }
			case "write-after":
				s.volumeSet = func(v int) error { originalSet(v); return errors.New("响应丢失") }
			}
			ctx, closeReply := s.WithTTSDuckReply(context.Background())
			done := startDuckPlayback(t, s, ctx)
			if mode == "manual-write" {
				s.volumeSet = func(int) error { return errors.New("失败") }
				if _, _, err := s.AdjustMusicVolume(-3); err == nil {
					t.Fatal("应返回失败")
				}
			}
			s.volumeGet, s.volumeSet = originalGet, originalSet
			done()
			closeReply()
			if *vol != 20 {
				t.Fatalf("失败后音量应恢复，got %d", *vol)
			}
			if (mode == "read" || mode == "persist" || mode == "write-before") && len(*writes) != 0 {
				t.Fatalf("不该写音量=%v", *writes)
			}
		})
	}
}

func TestTTSDuckCrashRecoveryAndRetry(t *testing.T) {
	for _, mode := range []string{"normal", "external", "read-fail", "restore-fail"} {
		t.Run(mode, func(t *testing.T) {
			s, vol, _ := newTTSDuckTestSpeaker(t, 20)
			ctx, _ := s.WithTTSDuckReply(context.Background())
			startDuckPlayback(t, s, ctx)()
			restarted := NewSpeaker()
			restarted.SetTTSMusicDuckFile(s.ttsMusicDuck.file)
			restarted.volumeGet, restarted.volumeSet = s.volumeGet, s.volumeSet
			switch mode {
			case "external":
				*vol = 8
			case "read-fail":
				restarted.volumeGet = func() (int, error) { return 0, errors.New("离线") }
			case "restore-fail":
				restarted.volumeSet = func(int) error { return errors.New("离线") }
			}
			restored := restarted.RestorePendingTTSMusicDuck()
			if mode == "read-fail" || mode == "restore-fail" {
				if restored {
					t.Fatal("失败不应报告恢复成功")
				}
				if _, err := os.Stat(s.ttsMusicDuck.file); err != nil {
					t.Fatal("失败时必须保留恢复记录")
				}
				restarted.volumeGet, restarted.volumeSet = s.volumeGet, s.volumeSet
				if !restarted.RestorePendingTTSMusicDuck() {
					t.Fatal("重试恢复失败")
				}
			}
			want := 20
			if mode == "external" {
				want = 8
			}
			if *vol != want {
				t.Fatalf("恢复=%d want %d", *vol, want)
			}
			if _, err := os.Stat(s.ttsMusicDuck.file); !os.IsNotExist(err) {
				t.Fatal("已恢复应删除记录")
			}
		})
	}
}

func TestTTSDuckReconnectDoesNotReleaseActiveReply(t *testing.T) {
	s, vol, _ := newTTSDuckTestSpeaker(t, 20)
	ctx, closeReply := s.WithTTSDuckReply(context.Background())
	startDuckPlayback(t, s, ctx)()
	s.RestorePendingMusicDuck()
	if *vol != 10 {
		t.Fatal("重连不能提前恢复正在播报的音量")
	}
	closeReply()
	if *vol != 20 {
		t.Fatal("播报结束应恢复")
	}
}

func TestTTSDuckRecoveryBeforeMusicSession(t *testing.T) {
	s, vol, _ := newTTSDuckTestSpeaker(t, 70)
	s.SetMusicDuckFile(filepath.Join(t.TempDir(), "music-duck.json"))
	s.SetMusicDuck(true, 20)
	s.DuckMusicVolume()
	ctx, _ := s.WithTTSDuckReply(context.Background())
	startDuckPlayback(t, s, ctx)()
	restarted := NewSpeaker()
	restarted.SetTTSMusicDuckFile(s.ttsMusicDuck.file)
	restarted.volumeGet, restarted.volumeSet = s.volumeGet, s.volumeSet
	restarted.RestorePendingMusicDuck()
	if *vol != 70 {
		t.Fatalf("重启时应先 10→20，再 20→70，got %d", *vol)
	}
}

// 用假的设备 RPC 验证真实流水线路径：不联网、不让音箱出声。
func TestTTSDuckPipelinePlaybackAndCancellation(t *testing.T) {
	s, vol, writes := newTTSDuckTestSpeaker(t, 20)
	s.nativeTakeover.active = true // 避免逐句 markSpeaking 的旧音乐恢复逻辑干扰本测试。
	rpc := connect.GetRPC()
	playing := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	var mu sync.Mutex
	scripts := []string{}
	rpc.Init(func(req connect.Request) error {
		var script string
		if req.Command == "run_shell" && req.Payload != nil {
			_ = json.Unmarshal(*req.Payload, &script)
		}
		mu.Lock()
		scripts = append(scripts, script)
		mu.Unlock()
		if strings.Contains(script, "aplay -D notify") {
			playing <- struct{}{}
			<-release
		}
		data, _ := json.Marshal(utils.CommandResult{})
		response := connect.DataResponse(data)
		response.ID = req.ID
		rpc.OnResponse(response)
		return nil
	})
	t.Cleanup(rpc.Dispose)
	ctx, cancel := context.WithCancel(context.Background())
	p := newSpeechPipeline(ctx, s)
	seg := &speechSeg{url: make(chan string, 1)}
	seg.url <- "http://example.invalid/tts/test.wav"
	p.in <- seg
	select {
	case <-playing:
	case <-time.After(3 * time.Second):
		t.Fatal("未进入播放")
	}
	if *vol != 10 {
		t.Fatal("真正播放前应压低音乐")
	}
	cancel()
	p.Close()
	if *vol != 10 {
		t.Fatal("取消时 RPC 仍未结束，不能恢复")
	}
	release <- struct{}{}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("播放未结束")
	}
	if *vol != 20 || !reflect.DeepEqual(*writes, []int{10, 20}) {
		t.Fatalf("恢复错误 %v", *writes)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, script := range scripts {
		for _, forbidden := range []string{"mphelper pause", "mphelper play", "player_play_url", "player_play_operation", "mysoftvol"} {
			if strings.Contains(script, forbidden) {
				t.Errorf("临时音量不该操作播放器: %s", script)
			}
		}
	}
}

func TestTTSDuckDefaultsAndConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.TTS.MusicDuck.Enabled || cfg.TTS.MusicDuck.Percent != 50 {
		t.Fatalf("默认配置错误: %+v", cfg.TTS.MusicDuck)
	}
	cfg, err := loadConfigFromBytes([]byte("tts:\n  music_duck:\n    enabled: true\n    percent: 40\n"))
	if err != nil || !cfg.TTS.MusicDuck.Enabled || cfg.TTS.MusicDuck.Percent != 40 {
		t.Fatalf("配置解析错误: %+v %v", cfg, err)
	}
}

func TestTTSDuckMuteWithoutMusicSessionDuck(t *testing.T) {
	s, vol, _ := newTTSDuckTestSpeaker(t, 20)
	ctx, closeReply := s.WithTTSDuckReply(context.Background())
	startDuckPlayback(t, s, ctx)()
	if _, _, err := s.SetMusicMuted(true); err != nil {
		t.Fatal(err)
	}
	closeReply()
	if *vol != 0 {
		t.Fatal("回答结束不能解除用户静音")
	}
	if _, _, err := s.SetMusicMuted(false); err != nil {
		t.Fatal(err)
	}
	if *vol != 20 {
		t.Fatalf("取消静音不能卡在临时压低的值: %d", *vol)
	}
}

func TestTTSDuckFailedStopRecoveryRetriesBothLayers(t *testing.T) {
	s, vol, _ := newTTSDuckTestSpeaker(t, 70)
	s.SetMusicDuckFile(filepath.Join(t.TempDir(), "music-duck.json"))
	s.SetMusicDuck(true, 20)
	s.DuckMusicVolume()
	ctx, closeReply := s.WithTTSDuckReply(context.Background())
	done := startDuckPlayback(t, s, ctx)
	setter := s.volumeSet
	s.volumeSet = func(int) error { return errors.New("设备断开") }
	s.st.music = false
	s.RestoreMusicVolume()
	done()
	closeReply()
	if *vol != 10 {
		t.Fatal("写失败不应虚报恢复")
	}
	s.volumeSet = setter
	s.RestorePendingMusicDuck()
	if *vol != 70 {
		t.Fatalf("重连后两层都应恢复，got %d", *vol)
	}
}

func TestTTSDuckNextReplyRecoversStaleRecordBeforeLowering(t *testing.T) {
	s, vol, _ := newTTSDuckTestSpeaker(t, 20)
	ctx, _ := s.WithTTSDuckReply(context.Background())
	startDuckPlayback(t, s, ctx)()
	restarted := NewSpeaker()
	restarted.st.music = true
	restarted.SetTTSMusicDuckFile(s.ttsMusicDuck.file)
	restarted.SetTTSMusicDuck(TTSMusicDuckConfig{Enabled: true, Percent: 50})
	restarted.volumeGet, restarted.volumeSet = s.volumeGet, s.volumeSet
	next, closeReply := restarted.WithTTSDuckReply(context.Background())
	startDuckPlayback(t, restarted, next)()
	if *vol != 10 {
		t.Fatalf("不能基于遗留的 10 再次压低: %d", *vol)
	}
	closeReply()
	if *vol != 20 {
		t.Fatalf("最终恢复错误: %d", *vol)
	}
}

func TestTTSDuckIgnoresInvalidRecoveryRecord(t *testing.T) {
	for _, raw := range []string{`{}`, `bad`, `{"original":101,"applied":10}`, `{"original":20,"applied":-1}`, `{"original":10,"applied":20}`} {
		s, vol, writes := newTTSDuckTestSpeaker(t, 20)
		if err := os.WriteFile(s.ttsMusicDuck.file, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		s.RestorePendingTTSMusicDuck()
		if *vol != 20 || len(*writes) != 0 {
			t.Fatalf("无效记录不应改音量: %s", raw)
		}
	}
}

func TestTTSDuckPlaybackFailureAndOtherRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, url, device string
		exit              int
		wantWrites        []int
	}{
		{"播放失败", "http://example.invalid/tts/a.wav", "notify", 1, []int{10, 20}},
		{"独立试听", "http://example.invalid/tts/a.wav", "notify", 0, []int{10, 20}},
		{"旧Playback路由", "http://example.invalid/tts/a.wav", "playback", 0, []int{}},
		{"音乐URL", "http://example.invalid/music.mp3", "notify", 0, []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, vol, writes := newTTSDuckTestSpeaker(t, 20)
			s.nativeTakeover.active = true
			s.SetTTSPlayback("auto", 60, tc.device)
			rpc := connect.GetRPC()
			rpc.Init(func(req connect.Request) error {
				var script string
				if req.Payload != nil {
					_ = json.Unmarshal(*req.Payload, &script)
				}
				result := utils.CommandResult{}
				if strings.Contains(script, "aplay") {
					result.ExitCode = tc.exit
				}
				data, _ := json.Marshal(result)
				response := connect.DataResponse(data)
				response.ID = req.ID
				rpc.OnResponse(response)
				return nil
			})
			t.Cleanup(rpc.Dispose)
			err := s.PlayURL(tc.url, tc.name != "音乐URL")
			if (err != nil) != (tc.exit != 0) {
				t.Fatalf("播放错误=%v", err)
			}
			if *vol != 20 || !reflect.DeepEqual(*writes, tc.wantWrites) {
				t.Fatalf("恢复或路由错误: %v", *writes)
			}
		})
	}
}
