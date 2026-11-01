package main

// 管理端：本机定时任务。
//
//	GET    /admin/api/tasks              列表
//	POST   /admin/api/tasks              {"text","schedule","ai"}
//	DELETE /admin/api/tasks?id=xxx       删除
//	POST   /admin/api/tasks/action       {"id","action":"run|enable|disable"}

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (a *adminServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	if a.tasks == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "定时任务未初始化")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeAdminJSON(w, http.StatusOK, map[string]any{"tasks": a.tasks.List()})

	case http.MethodPost:
		var body struct {
			Text     string `json:"text"`
			Schedule string `json:"schedule"`
			AI       bool   `json:"ai"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		task, err := a.tasks.Add(body.Text, body.Schedule, body.AI)
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"task": task})

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeAdminError(w, http.StatusBadRequest, "缺少 id")
			return
		}
		if !a.tasks.Delete(id) {
			writeAdminError(w, http.StatusNotFound, "任务不存在")
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

func (a *adminServer) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	if a.tasks == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "定时任务未初始化")
		return
	}
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		writeAdminError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	switch body.Action {
	case "run":
		if a.runTask == nil {
			writeAdminError(w, http.StatusServiceUnavailable, "执行器未就绪")
			return
		}
		if err := a.runTask(id); err != nil {
			writeAdminError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "stop":
		if a.stopTask == nil {
			writeAdminError(w, http.StatusServiceUnavailable, "执行器未就绪")
			return
		}
		if !a.stopTask(id) {
			writeAdminError(w, http.StatusConflict, "该任务当前没有在执行")
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "enable", "disable":
		task, ok := a.tasks.SetEnabled(id, body.Action == "enable")
		if !ok {
			writeAdminError(w, http.StatusNotFound, "任务不存在")
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"task": task})
	default:
		writeAdminError(w, http.StatusBadRequest, "action 只支持 run/enable/disable")
	}
}
