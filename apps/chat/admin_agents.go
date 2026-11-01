package main

// 管理台「智能体」页：分别管理三条 codex thread 的上下文。
//
//	GET  /admin/api/agents          列出三条会话的 thread id 和状态
//	POST /admin/api/agents/action   {"name":"dialog|router|tasks","action":"compact|reset"}
//
// 不自动压缩：什么时候压、要不要重开，人说了算。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type agentInfo struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Desc     string `json:"desc"`
	ThreadID string `json:"thread_id"`
	Ready    bool   `json:"ready"`
	Turns    int    `json:"turns"` // 上下文里有多少轮，-1 表示读不到
}

func (a *adminServer) agents() []agentInfo {
	var out []agentInfo

	srv := a.codexServer
	if srv != nil {
		out = append(out, agentInfo{
			Name:     "dialog",
			Label:    "主对话",
			Desc:     "平时聊天、问答走这条会话，有完整上下文和全部工具",
			ThreadID: srv.CurrentThread(),
			Ready:    srv.CurrentThread() != "",
		})
	}
	if a.engine != nil && a.engine.codexRouter != nil {
		id := a.engine.codexRouter.CurrentThread()
		out = append(out, agentInfo{
			Name:     "router",
			Label:    "音乐路由",
			Desc:     "放音乐时带唤醒词的话先进这里；工具和主对话一样全，但只办事不聊天",
			ThreadID: id,
			Ready:    id != "",
		})
	}
	if a.codexServer != nil && a.taskRunner != nil {
		if r, ok := a.taskRunner.(*CodexThreadRunner); ok {
			id := r.CurrentThread()
			out = append(out, agentInfo{
				Name:     "tasks",
				Label:    "定时任务",
				Desc:     "到点执行的 AI 任务用这条会话，和对话上下文互不干扰",
				ThreadID: id,
				Ready:    id != "",
			})
		}
	}
	return out
}

// agentTurns 会话里累积了多少轮；读不到返回 -1（不阻塞页面）。
func (a *adminServer) agentTurns(threadID string) int {
	if threadID == "" || a.codexServer == nil {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := a.codexServer.ListTurns(ctx, threadID)
	if err != nil {
		return -1
	}
	// 形状可能是数组，也可能包在 {turns:...} / {items:...} 里
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		return len(arr)
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		return -1
	}
	for _, key := range []string{"turns", "items", "data", "turnList"} {
		if inner, ok := v[key]; ok {
			var list []json.RawMessage
			if json.Unmarshal(inner, &list) == nil {
				return len(list)
			}
			var wrap struct {
				Turns []json.RawMessage `json:"turns"`
				Items []json.RawMessage `json:"items"`
			}
			if json.Unmarshal(inner, &wrap) == nil {
				if wrap.Turns != nil {
					return len(wrap.Turns)
				}
				if wrap.Items != nil {
					return len(wrap.Items)
				}
			}
		}
	}
	return -1
}

func (a *adminServer) handleAgents(w http.ResponseWriter, r *http.Request) {
	if a.codexServer == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "当前不是 codex 后端（chat.provider 不是 codex）",
			"agents":  []agentInfo{},
		})
		return
	}
	agents := a.agents()
	for i := range agents {
		agents[i].Turns = a.agentTurns(agents[i].ThreadID)
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"agents":  agents,
	})
}

func (a *adminServer) handleAgentAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	var req struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}
	srv := a.codexServer
	if srv == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "codex 后端未启用")
		return
	}
	react := func(name string, err error, okMsg string) {
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"message": okMsg})
	}

	// 取当前 thread id
	current := ""
	reset := func() {}
	switch req.Name {
	case "dialog":
		current = srv.CurrentThread()
		reset = srv.ResetMainThread
	case "router":
		if a.engine == nil || a.engine.codexRouter == nil {
			writeAdminError(w, http.StatusServiceUnavailable, "路由会话未启用")
			return
		}
		current = a.engine.codexRouter.CurrentThread()
		reset = a.engine.codexRouter.Reset
	case "tasks":
		r, ok := a.taskRunner.(*CodexThreadRunner)
		if !ok {
			writeAdminError(w, http.StatusServiceUnavailable, "任务会话不可用")
			return
		}
		current = r.CurrentThread()
		reset = r.Reset
	default:
		writeAdminError(w, http.StatusBadRequest, "name 只能是 dialog / router / tasks")
		return
	}

	switch strings.TrimSpace(req.Action) {
	case "reset":
		reset()
		writeAdminJSON(w, http.StatusOK, map[string]any{"message": "已丢弃该会话，下次用到时自动新建"})
	case "compact":
		if current == "" {
			writeAdminError(w, http.StatusBadRequest, "该会话还没建立，先聊一句再压缩")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		react(req.Name, srv.CompactThread(ctx, current), "已请求压缩该会话的历史")
	default:
		writeAdminError(w, http.StatusBadRequest, "action 只能是 compact / reset")
	}
}
