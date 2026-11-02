package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

func mcpPost(t *testing.T, a *adminServer, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	a.handleMCP(rec, req)

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestMCPInitializeAndTools(t *testing.T) {
	a := &adminServer{}

	code, resp := mcpPost(t, a, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if code != http.StatusOK {
		t.Fatalf("initialize status = %d", code)
	}
	result, _ := resp["result"].(map[string]any)
	if result["protocolVersion"] == "" {
		t.Fatalf("缺 protocolVersion: %v", resp)
	}

	code, resp = mcpPost(t, a, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list status = %d", code)
	}
	result, _ = resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, it := range tools {
		if m, ok := it.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				names[n] = true
			}
		}
	}
	for _, want := range []string{"play_music", "stop_music", "speak", "get_time"} {
		if !names[want] {
			t.Errorf("工具列表缺少 %s (got %v)", want, names)
		}
	}
}

func TestVolumeToolsDistinguishMusicAndTTS(t *testing.T) {
	byName := map[string]mcpTool{}
	for _, tool := range mcpToolList() {
		byName[tool.Name] = tool
	}
	music, ok := byName["set_volume"]
	if !ok {
		t.Fatal("缺少 set_volume")
	}
	tts, ok := byName["set_tts_volume"]
	if !ok {
		t.Fatal("缺少 set_tts_volume")
	}
	// 泛说“声音”默认音乐，必须指向另一个工具。
	for _, want := range []string{"音乐", "默认", "set_tts_volume"} {
		if !strings.Contains(music.Description, want) {
			t.Errorf("set_volume 描述缺少 %q: %s", want, music.Description)
		}
	}
	// “说话声/豆包声音”才是 TTS，并且要指回音乐工具。
	for _, want := range []string{"说话声", "豆包", "set_volume", "音乐"} {
		if !strings.Contains(tts.Description, want) {
			t.Errorf("set_tts_volume 描述缺少 %q: %s", want, tts.Description)
		}
	}
}

func TestMCPNotificationsAccepted(t *testing.T) {
	a := &adminServer{}
	code, _ := mcpPost(t, a, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != http.StatusAccepted {
		t.Fatalf("通知应返回 202，实际 %d", code)
	}
}

func TestMCPGetTime(t *testing.T) {
	a := &adminServer{}
	code, resp := mcpPost(t, a, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_time","arguments":{}}}`)
	if code != http.StatusOK {
		t.Fatalf("tools/call status = %d", code)
	}
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("没有 content: %v", resp)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "-") || !strings.Contains(text, ":") {
		t.Fatalf("时间格式异常: %q", text)
	}
}

func TestMCPUnknownTool(t *testing.T) {
	a := &adminServer{}
	_, resp := mcpPost(t, a, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	result, _ := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("未知工具应返回 isError: %v", resp)
	}
}

func TestRankSongsPrefersOriginal(t *testing.T) {
	songs := []netease.Song{
		{ID: 1, Name: "晴天 (原唱 周杰伦)", Artists: "RyaVocal", Duration: 269000},
		{ID: 2, Name: "晴天", Artists: "周杰伦", Duration: 269000},
		{ID: 3, Name: "晴天 (翻唱)", Artists: "某某", Duration: 260000},
	}
	got := rankSongs("周杰伦 晴天", songs)
	if got[0].ID != 2 {
		t.Fatalf("应优先原唱，实际第一首: %+v", got[0])
	}
	if got[len(got)-1].ID != 3 {
		t.Fatalf("翻唱应排最后，实际: %+v", got[len(got)-1])
	}
}
