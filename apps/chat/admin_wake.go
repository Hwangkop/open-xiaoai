package main

// 管理端：唤醒词管理。
//
//	GET    /admin/api/wake          列出用户自定义词 + 内置词
//	POST   /admin/api/wake          {"word":"豆包包包"} 添加
//	DELETE /admin/api/wake?word=xx  删除
//	GET    /admin/api/wake/test?text=xx  测试一句话能否唤醒
//
// 自定义词落在 wake-words.json，热生效不用重启。

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleWakeRecent 返回最近的识别结果，便于发现"被听成了什么"。
func (a *adminServer) handleWakeRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		clearUtterances()
		writeAdminJSON(w, http.StatusOK, map[string]any{"cleared": true})
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"items": listUtterances()})
}

func (a *adminServer) handleWakeWords(w http.ResponseWriter, r *http.Request) {
	if a.getWakeStore == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "唤醒词存储未初始化")
		return
	}
	store := a.getWakeStore()

	switch r.Method {
	case http.MethodGet:
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"custom":   store.List(),
			"defaults": defaultWakeWords,
			"weak":     weakWakeWords,
		})

	case http.MethodPost:
		var req struct {
			Word string `json:"word"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
			return
		}
		// 存之前统一去标点：界面里点「加为唤醒词」带出来的句号、引号都清掉
		word := stripPunct(req.Word)
		if word == "" {
			writeAdminError(w, http.StatusBadRequest, "唤醒词不能为空")
			return
		}
		added, err := store.Add(word)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"added":   added,
			"custom":  store.List(),
			"message": map[bool]string{true: "已添加", false: "已存在"}[added],
		})

	case http.MethodDelete:
		word := strings.TrimSpace(r.URL.Query().Get("word"))
		if word == "" {
			writeAdminError(w, http.StatusBadRequest, "缺少 word 参数")
			return
		}
		removed, err := store.Remove(word)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"removed": removed,
			"custom":  store.List(),
		})

	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// handleRouteRecent 返回音乐态路由漏斗：每句话走到哪一层、判成什么。
//
//	GET    /admin/api/route/recent   {stats:{...}, items:[...]}
//	DELETE /admin/api/route/recent   清空
func (a *adminServer) handleRouteRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		clearRouteLog()
		writeAdminJSON(w, http.StatusOK, map[string]any{"cleared": true})
		return
	}
	items, stats := routeLogSnapshot()
	writeAdminJSON(w, http.StatusOK, map[string]any{"stats": stats, "items": items})
}
