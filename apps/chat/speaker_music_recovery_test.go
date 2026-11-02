package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
	"github.com/cxjava/open-xiaoai/apps/client/utils"
)

func TestResumeMusicDoesNotReplayAnAlreadyPlayingStream(t *testing.T) {
	s := NewSpeaker()
	s.st.music = true
	s.st.musicURL = "http://example.invalid/current.mp3"
	rpc := connect.GetRPC()
	var mu sync.Mutex
	var scripts []string
	rpc.Init(func(req connect.Request) error {
		var script string
		if req.Command == "run_shell" && req.Payload != nil {
			_ = json.Unmarshal(*req.Payload, &script)
		}
		mu.Lock()
		scripts = append(scripts, script)
		mu.Unlock()
		result := utils.CommandResult{Stdout: `{"code":0,"info":"{\"status\":1,\"volume\":20}"}`}
		data, _ := json.Marshal(result)
		resp := connect.DataResponse(data)
		resp.ID = req.ID
		rpc.OnResponse(resp)
		return nil
	})
	t.Cleanup(rpc.Dispose)
	s.resumeMusicAfterSpeech()
	mu.Lock()
	defer mu.Unlock()
	queried := false
	for _, script := range scripts {
		if strings.Contains(script, "player_get_play_status") {
			queried = true
		}
		if strings.Contains(script, "mphelper play") || strings.Contains(script, "player_play_url") || strings.Contains(script, "player_set_positon") {
			t.Errorf("已在播放的流不能重新 play/重拉/seek：%s", script)
		}
	}
	if !queried {
		t.Fatal("恢复前必须先查询真实状态")
	}
}
