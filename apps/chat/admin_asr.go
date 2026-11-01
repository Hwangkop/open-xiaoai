package main

// 管理端：语音识别引擎切换。
//
//	GET  /admin/api/asr   当前 provider 与可用列表
//	POST /admin/api/asr   {"provider":"local_alt"} 切换对话识别
//	                     {"wake_provider":"local"} 切换唤醒识别

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleWakeTest 在线测试一句话能否命中唤醒词：
//
//	GET /admin/api/wake/test?text=豆包豆包
func (a *adminServer) handleWakeTest(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	cfg := a.getConfig()
	if cfg == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "配置未加载")
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"text":   text,
		"match":  cfg.IsAIWakeWord(text) || a.wakeMatch(text),
		"pinyin": toSyllables(text),
		"rest":   stripWakePrefix(text, cfg, a.wakeWords()),
		"ack":    len([]rune(stripWakePrefix(text, cfg, a.wakeWords()))) <= 3,
	})
}

// wakeWords / wakeMatch 是给测试接口用的小包装。
func (a *adminServer) wakeWords() []string {
	if a.getWakeStore == nil {
		return nil
	}
	if s := a.getWakeStore(); s != nil {
		return s.List()
	}
	return nil
}

func (a *adminServer) wakeMatch(text string) bool {
	if a.getWakeStore == nil {
		return false
	}
	if s := a.getWakeStore(); s != nil {
		return s.Contains(text)
	}
	return false
}

func (a *adminServer) handleASR(w http.ResponseWriter, r *http.Request) {
	router := a.getASRRouter()
	if router == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "服务端唤醒未启用（config.yaml 里 asr.enabled: true 并配置 api_key）",
		})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Provider     string `json:"provider"`
			WakeProvider string `json:"wake_provider"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
			return
		}
		if p := strings.TrimSpace(req.WakeProvider); p != "" {
			if err := router.SetWakeProvider(p); err != nil {
				writeAdminError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if p := strings.TrimSpace(req.Provider); p != "" {
			if err := router.SetDialogProvider(p); err != nil {
				writeAdminError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}

	out := router.Status()
	out["enabled"] = true
	writeAdminJSON(w, http.StatusOK, out)
}
