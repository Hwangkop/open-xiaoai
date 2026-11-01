package main

// 管理端：对话测试（跳过麦克风/唤醒词，直接把一句话交给 AI 引擎）。
//
//	POST /admin/api/chat  {"text":"播放周杰伦的晴天"}
//
// 用来远程验证「AI 回复 + 工具调用 + 音乐」整条链路，不用对着音箱说话。

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (a *adminServer) handleChatTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeAdminError(w, http.StatusBadRequest, "text 不能为空")
		return
	}
	if a.chatTest == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "对话引擎未就绪")
		return
	}
	a.chatTest(text)
	writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
}
