package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type musicURLTransport struct{}

func (musicURLTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"code":200,"data":[{"url":"http://example.invalid/song.mp3"}]}`
	switch {
	case strings.Contains(req.URL.Path, "search"):
		body = `{"code":200,"result":{"songs":[{"id":101,"name":"晴天"},{"id":102,"name":"晴天伴奏"}]}}`
	case strings.Contains(req.URL.Path, "recommend/songs"):
		body = `{"code":200,"data":{"dailySongs":[{"id":201,"name":"推荐一"},{"id":202,"name":"推荐二"}]}}`
	case strings.Contains(req.URL.Path, "account/get"):
		body = `{"code":200,"profile":{"userId":1}}`
	case strings.Contains(req.URL.Path, "user/playlist"):
		body = `{"code":200,"playlist":[{"id":1,"name":"我喜欢的音乐","specialType":5}]}`
	case strings.Contains(req.URL.Path, "playlist/detail"):
		body = `{"code":200,"playlist":{"tracks":[{"id":301,"name":"喜欢一"},{"id":302,"name":"喜欢二"}]}}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func musicSyncAdmin(t *testing.T, deviceReply func(string) string) *adminServer {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = musicURLTransport{}
	t.Cleanup(func() { http.DefaultTransport = original })
	s := NewSpeaker()
	s.st.duckEnabled = false
	s.st.musicTracking = true // 测试手动驱动轮询，保留真实选曲与设备 RPC 路径。
	mockMusicDevice(t, deviceReply)
	a := newAdminServer(&appRuntime{speaker: s, configPath: filepath.Join(t.TempDir(), "config.yaml")})
	a.music.lastSearch = testSongs(5)
	return a
}

func seedMusicPlaylist(t *testing.T, a *adminServer, index int) {
	t.Helper()
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	if _, err := a.playSong(context.Background(), testSongs(5), index, "我喜欢"); err != nil {
		t.Fatal(err)
	}
}

func finishTestMusic(a *adminServer) {
	a.speaker.stMu.Lock()
	a.speaker.st.musicSession.graceUntil = time.Time{}
	a.speaker.stMu.Unlock()
	a.speaker.pollMusic()
	a.speaker.pollMusic()
}

func TestAgentOneShotSurvivesSpeechAndReturnsToPlaylist(t *testing.T) {
	status := 1
	recovered := make(chan struct{}, 1)
	a := musicSyncAdmin(t, func(script string) string {
		if strings.Contains(script, "player_get_play_status") {
			select {
			case recovered <- struct{}{}:
			default:
			}
			return fmt.Sprintf(`{"info":{"status":%d}}`, status)
		}
		return `{"code":0}`
	})
	seedMusicPlaylist(t, a, 2)
	for i := range a.music.lastSearch {
		a.music.lastSearch[i].ID += 100
	}
	if _, err := a.runMCPTool(context.Background(), mcpToolCall{Name: "play_music", Arguments: map[string]any{"index": float64(3)}}); err != nil {
		t.Fatal(err)
	}
	assertIndex := func(index int, songID int64, oneShot bool) {
		t.Helper()
		if a.music.idx != index || a.music.current == nil || a.music.current.ID != songID || a.music.oneShot != oneShot {
			t.Fatalf("歌曲与序号不一致: index=%d current=%+v", a.music.idx, a.music.current)
		}
		raw, err := os.ReadFile(a.music.path)
		if err != nil {
			t.Fatal(err)
		}
		var snap musicSnapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatal(err)
		}
		if snap.Index != index || snap.Current == nil || snap.Current.ID != songID || snap.Queue[index].ID != int64(index+1) || snap.OneShot != oneShot {
			t.Fatalf("落盘记录不一致: %+v", snap)
		}
	}
	assertIndex(2, 103, true) // MCP 序号从 1 开始，内部从 0 开始。
	a.speaker.markSpeaking(true)
	a.speaker.pollMusic()
	a.speaker.ClearPendingMusic() // 模拟旧对话取消时的兼容清理。
	assertIndex(2, 103, true)
	a.speaker.markSpeaking(false)
	select {
	case <-recovered:
	case <-time.After(2 * time.Second):
		t.Fatal("播报后没有检查音乐恢复")
	}
	a.speaker.musicActionMu.Lock() // 等本轮恢复结束。
	a.speaker.musicActionMu.Unlock()
	assertIndex(2, 103, true)
	a.speaker.stMu.Lock()
	a.speaker.st.speakingCool = time.Time{}
	a.speaker.st.musicSession.graceUntil = time.Time{}
	a.speaker.stMu.Unlock()
	status = 3
	a.speaker.pollMusic()
	a.speaker.pollMusic()
	assertIndex(3, 4, false)
	restored := &adminServer{music: newMusicState()}
	restored.loadMusic(a.music.path)
	if restored.music.idx != 3 || restored.music.current.ID != 4 {
		t.Fatal("重启后没有恢复最新播放序号")
	}
}

func TestFailedAgentSelectionKeepsCurrentQueueAndIndex(t *testing.T) {
	a := musicSyncAdmin(t, func(string) string { return `{"code":0}` })
	seedMusicPlaylist(t, a, 2)
	before, err := os.ReadFile(a.music.path)
	if err != nil {
		t.Fatal(err)
	}
	a.playURL = func(string) error { return fmt.Errorf("模拟音箱下发失败") }
	a.music.lastSearch = testSongs(2)
	if _, err := a.playSearchIndex(context.Background(), 1); err == nil {
		t.Fatal("下发失败不能报告起播成功")
	}
	if a.music.idx != 2 || a.music.current.ID != 3 || len(a.music.queue) != 5 {
		t.Fatal("失败的选曲覆盖了正在播放的队列")
	}
	after, err := os.ReadFile(a.music.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("失败的选曲不应提交新快照")
	}
}

func TestSearchQueryPlaysOneSongThenReturnsToPlaylist(t *testing.T) {
	a := musicSyncAdmin(t, func(string) string { return `{"code":0,"info":{"status":3}}` })
	seedMusicPlaylist(t, a, 2)
	if _, err := a.runMCPTool(context.Background(), mcpToolCall{Name: "play_music", Arguments: map[string]any{"query": "晴天"}}); err != nil {
		t.Fatal(err)
	}
	if !a.music.oneShot || a.music.current.ID != 101 || a.music.idx != 2 || len(a.music.queue) != 5 {
		t.Fatal("搜索点播覆盖了原歌单")
	}
	finishTestMusic(a)
	if a.music.oneShot || a.music.current.ID != 4 || a.music.idx != 3 {
		t.Fatal("点播结束后没有回到原歌单第 4 首")
	}
}

func TestOneShotWithoutPlaylistStopsAndCanBeReplayed(t *testing.T) {
	a := musicSyncAdmin(t, func(string) string { return `{"info":{"status":3}}` })
	if _, err := a.playSearchIndex(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	finishTestMusic(a)
	if a.speaker.MusicPlaying() || len(a.music.queue) != 0 || a.music.current.ID != 3 {
		t.Fatal("没有原歌单时只能播放点播歌曲，不能继续候选")
	}
	if err := a.controlQueuedMusic(context.Background(), "resume", nil); err != nil {
		t.Fatal(err)
	}
	if !a.music.oneShot || a.music.current.ID != 3 || !a.speaker.MusicPlaying() {
		t.Fatal("继续播放没有恢复点播歌曲")
	}
}

func TestRepeatedOneShotsKeepOriginalPlaylistAndManualNextReturns(t *testing.T) {
	a := musicSyncAdmin(t, func(string) string { return `{"code":0}` })
	seedMusicPlaylist(t, a, 2)
	for _, index := range []int{1, 2, 5} {
		if _, err := a.playSearchIndex(context.Background(), index); err != nil {
			t.Fatal(err)
		}
		if a.music.idx != 2 || a.music.queueName != "我喜欢" || len(a.music.queue) != 5 {
			t.Fatal("连续点播修改了原歌单位置")
		}
	}
	if _, err := a.runMCPTool(context.Background(), mcpToolCall{Name: "next_music"}); err != nil {
		t.Fatal(err)
	}
	if a.music.oneShot || a.music.idx != 3 || a.music.current.ID != 4 {
		t.Fatal("下一首没有跳过点播返回原歌单")
	}
}

func TestOneShotSnapshotRestoresReturnPlaylist(t *testing.T) {
	a := musicSyncAdmin(t, func(string) string { return `{"code":0}` })
	seedMusicPlaylist(t, a, 2)
	if _, err := a.playSearchIndex(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	// 模拟进程重启：重新读取快照，继续使用假的设备与网易云接口。
	a.music = newMusicState()
	a.loadMusic(filepath.Join(filepath.Dir(a.configPath), "music-queue.json"))
	if !a.music.oneShot || a.music.current.ID != 1 || a.music.idx != 2 || a.music.queueName != "我喜欢" {
		t.Fatal("点播快照没有保存原歌单与点播状态")
	}
	if _, err := a.nextMusic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.music.current.ID != 4 || a.music.oneShot {
		t.Fatal("快照恢复后没有接回原歌单")
	}
}

func TestStoppingOneShotDoesNotAutomaticallyResumePlaylist(t *testing.T) {
	a := musicSyncAdmin(t, func(string) string { return `{"code":0,"info":{"status":3}}` })
	seedMusicPlaylist(t, a, 2)
	if _, err := a.playSearchIndex(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	a.stopPlayback()
	finishTestMusic(a)
	if a.speaker.MusicPlaying() || !a.music.oneShot || a.music.idx != 2 || a.music.current.ID != 1 {
		t.Fatal("停止点播后不能自动启动原歌单")
	}
}

func TestExplicitPlaylistSwitchReplacesOneShotReturnTarget(t *testing.T) {
	for _, action := range []string{"play_daily", "play_playlist"} {
		t.Run(action, func(t *testing.T) {
			a := musicSyncAdmin(t, func(string) string { return `{"code":0}` })
			seedMusicPlaylist(t, a, 2)
			if _, err := a.playSearchIndex(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			a.getNeteaseStore = func() *neteaseStore { return &neteaseStore{sess: neteaseSession{Cookie: "MUSIC_U=test-only"}} }
			if _, err := a.runMCPTool(context.Background(), mcpToolCall{Name: action, Arguments: map[string]any{"name": "我喜欢", "shuffle": false}}); err != nil {
				t.Fatal(err)
			}
			if a.music.oneShot || len(a.music.queue) != 2 {
				t.Fatal("明确切换歌单后仍然保留了旧点播")
			}
			want := a.music.queue[1].ID
			if _, err := a.nextMusic(context.Background()); err != nil {
				t.Fatal(err)
			}
			if a.music.current.ID != want {
				t.Fatal("切换歌单后回到了旧歌单")
			}
		})
	}
}

func TestLegacySearchQueueIsRestoredAsSingleSong(t *testing.T) {
	for _, name := range []string{"搜索：晴天", "搜索候选"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "music-queue.json")
			songs := testSongs(5)
			raw, _ := json.Marshal(musicSnapshot{Queue: songs, Index: 2, QueueName: name, Current: &songs[2]})
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			a := &adminServer{music: newMusicState()}
			a.loadMusic(path)
			if !a.music.oneShot || len(a.music.queue) != 0 || a.music.current.ID != 3 {
				t.Fatal("旧搜索列表不能作为持续歌单恢复")
			}
		})
	}
}
