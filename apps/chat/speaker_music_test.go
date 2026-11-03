package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
	"github.com/cxjava/open-xiaoai/apps/client/utils"
)

func mockMusicDevice(t *testing.T, reply func(string) string) {
	t.Helper()
	rpc := connect.GetRPC()
	rpc.Init(func(req connect.Request) error {
		var script string
		if req.Payload != nil {
			_ = json.Unmarshal(*req.Payload, &script)
		}
		data, _ := json.Marshal(utils.CommandResult{Stdout: reply(script)})
		resp := connect.DataResponse(data)
		resp.ID = req.ID
		rpc.OnResponse(resp)
		return nil
	})
	t.Cleanup(rpc.Dispose)
}

func managedTestSpeaker() *Speaker {
	s := NewSpeaker()
	s.st.music = true
	s.st.musicURL = "http://example.invalid/music.mp3"
	s.st.musicStart = time.Now().Add(-time.Minute)
	s.st.musicTracking = true // 测试手动轮询，不启动后台协程。
	s.st.musicSession.generation = 1
	return s
}

func TestMusicPauseAndDeviceResumePreserveAutoAdvance(t *testing.T) {
	s := managedTestSpeaker()
	status := 2
	mockMusicDevice(t, func(string) string {
		return `{"info":{"status":` + string(rune('0'+status)) + `}}`
	})
	advances := 0
	s.SetMusicEndedHandler(func(uint64) bool { advances++; return false })
	s.pollMusic()
	if s.MusicPlayback().Status != "paused" || !s.st.music || s.MusicPlaying() {
		t.Fatal("暂停必须保留会话并报告暂停")
	}
	for i := 0; i < 35; i++ {
		s.pollMusic()
	}
	if advances != 0 {
		t.Fatal("暂停不能切歌")
	}
	status = 1
	s.pollMusic()
	if !s.MusicPlaying() {
		t.Fatal("设备恢复后必须同步播放态")
	}
	status = 3
	s.pollMusic()
	if advances != 0 {
		t.Fatal("单次播完状态不能立即切歌")
	}
	s.pollMusic()
	if advances != 1 || s.MusicPlayback().Status != "ended" {
		t.Fatalf("恢复后必须仍能推进队列: advances=%d", advances)
	}
}

func TestMusicZeroStatusRequiresDeviceEvidenceOfEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		position int64
		duration int64
		advance  bool
	}{
		{"外部中断", 30000, 180000, false},
		{"没有设备进度", 0, 180000, false},
		{"未知时长", 179000, 0, false},
		{"确认曲尾", 179000, 180000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := managedTestSpeaker()
			s.st.musicSession.positionMs, s.st.musicSession.durationMs = tc.position, tc.duration
			mockMusicDevice(t, func(string) string { return `{"info":{"status":0}}` })
			advances := 0
			s.SetMusicEndedHandler(func(uint64) bool { advances++; return false })
			for i := 0; i < 35; i++ {
				s.pollMusic()
			}
			if (advances == 1) != tc.advance {
				t.Fatalf("advances=%d", advances)
			}
			if !tc.advance && (!s.st.music || s.MusicPlayback().Status != "stalled") {
				t.Fatal("中断不能销毁会话监控")
			}
		})
	}
}

func TestMusicStaleOrInterruptedDeviceResponseCannotAdvance(t *testing.T) {
	for _, kind := range []string{"换歌", "停止", "开始对话", "解析失败"} {
		t.Run(kind, func(t *testing.T) {
			s := managedTestSpeaker()
			mockMusicDevice(t, func(string) string {
				s.stMu.Lock()
				switch kind {
				case "换歌":
					s.st.musicSession.generation++
				case "停止":
					s.st.music = false
					s.st.musicSession.generation++
				case "开始对话":
					s.st.nativeASR = true
				}
				s.stMu.Unlock()
				if kind == "解析失败" {
					return `无效响应`
				}
				return `{"info":{"status":3}}`
			})
			s.SetMusicEndedHandler(func(uint64) bool { t.Fatal("旧响应/对话/无效响应不能切歌"); return false })
			s.pollMusic()
			s.pollMusic()
		})
	}
}

func TestMusicRecoveryPreservesProgressAndDoesNotRestartEndedTrack(t *testing.T) {
	s := managedTestSpeaker()
	start := s.st.musicStart
	mockMusicDevice(t, func(script string) string {
		if !strings.Contains(script, "player_get_play_status") {
			t.Fatalf("已经播完不能重新起播: %s", script)
		}
		return `{"info":{"status":3}}`
	})
	s.resumeMusicAfterSpeech()
	if s.st.musicStart != start {
		t.Fatal("恢复宽限期不能重置歌曲进度")
	}
	s.st.music = false
	s.resumeMusicAfterSpeech() // 停止后不能再向设备发恢复指令。
}

func TestMusicAdminStateDoesNotExposePlaybackURL(t *testing.T) {
	a := &adminServer{music: newMusicState(), speaker: NewSpeaker()}
	a.music.currentURL = "http://example.invalid/private-token"
	w := httptest.NewRecorder()
	a.handleMusicPlayer(w, httptest.NewRequest(http.MethodGet, "/admin/api/music", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "private-token") {
		t.Fatalf("播放状态返回错误: %s", w.Body.String())
	}
	for _, body := range []string{`{"action":"select"}`, `{"action":"select","index":-1}`, `{"action":"search","query":" "}`, `{"action":"unknown"}`} {
		w = httptest.NewRecorder()
		a.handleMusicPlayer(w, httptest.NewRequest(http.MethodPost, "/admin/api/music", strings.NewReader(body)))
		if w.Code < 400 {
			t.Fatalf("无效操作不能成功: %s", body)
		}
	}
}
