package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

// 音乐管理使用普通管理台鉴权；不依赖仅供本机调试的接口。
func (a *adminServer) handleMusicPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.music.mu.Lock()
		state := map[string]any{
			"queue":      append(a.music.queue[:0:0], a.music.queue...),
			"queue_name": a.music.queueName, "index": a.music.idx,
			"current": a.music.current, "error": a.music.lastError,
			"one_shot": a.music.oneShot,
		}
		state["source"] = "netease"
		bilibiliStatus := ""
		if q := a.music.bilibili; q != nil {
			items := make([]netease.Song, 0, len(q.Items))
			for i, item := range q.Items {
				items = append(items, netease.Song{ID: int64(i + 1), Name: item.Title, Artists: item.Author, Duration: item.Duration * 1000})
			}
			state["source"], state["queue"], state["queue_name"], state["index"] = "bilibili", items, q.Name, q.Index
			state["current"], state["one_shot"], state["error"] = items[q.Index], false, q.Error
			bilibiliStatus = q.Status
		}
		a.music.mu.Unlock()
		playback := a.speaker.MusicPlayback()
		switch bilibiliStatus {
		case "preparing":
			playback.Status, playback.PositionMs, playback.DurationMs = "loading", 0, 0
		case "error", "stopped", "ended":
			playback.Status = bilibiliStatus
		case "paused":
			if !a.speaker.MusicPlaying() {
				playback.Status = "paused"
			}
		}
		state["playback"] = playback
		writeAdminJSON(w, http.StatusOK, state)
		return
	}
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	var req struct {
		Action string `json:"action"`
		Query  string `json:"query"`
		Index  *int   `json:"index"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, adminMaxRequestBytes)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var msg string
	var err error
	switch req.Action {
	case "search":
		if strings.TrimSpace(req.Query) == "" {
			writeAdminError(w, http.StatusBadRequest, "请输入歌曲或歌手")
			return
		}
		msg, err = a.searchAndPlay(ctx, strings.TrimSpace(req.Query), false)
	case "daily":
		msg, err = a.playDaily(ctx)
	case "next":
		msg, err = a.nextMusic(ctx)
	case "stop":
		a.stopPlayback()
		msg = "已停止播放，队列已保留"
	case "pause", "resume", "replay", "select", "previous":
		err = a.controlQueuedMusic(ctx, req.Action, req.Index)
		msg = "播放指令已执行"
	default:
		writeAdminError(w, http.StatusBadRequest, "不支持的音乐操作")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, adminResponse{Message: msg})
}

func (a *adminServer) controlQueuedMusic(ctx context.Context, action string, index *int) error {
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	if handled, err := a.controlBilibiliLocked(action, index); handled {
		return err
	}
	if action == "pause" {
		return a.speaker.PauseMusic()
	}
	if action == "resume" && a.speaker.stateSnapshot().music {
		return a.speaker.ResumeMusic()
	}
	a.music.mu.Lock()
	idx, name := a.music.idx, a.music.queueName
	q := append(a.music.queue[:0:0], a.music.queue...)
	oneShot, current := a.music.oneShot, a.music.current
	a.music.mu.Unlock()
	if oneShot && current != nil && (action == "replay" || action == "resume") {
		_, err := a.playOneShot(ctx, *current)
		return err
	}
	if action == "select" {
		if index == nil {
			return fmt.Errorf("请选择歌曲")
		}
		idx = *index
	}
	if action == "previous" {
		idx--
	}
	if idx < 0 || idx >= len(q) {
		return fmt.Errorf("歌曲序号超出队列范围")
	}
	_, err := a.playSong(ctx, q, idx, name)
	return err
}
