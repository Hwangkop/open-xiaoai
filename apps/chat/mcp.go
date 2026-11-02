package main

// MCP 服务端（Streamable HTTP + JSON-RPC），给 codex 之类的 harness 提供工具。
//
// 端点：POST /mcp
// 支持的方法：initialize / notifications/initialized / tools/list / tools/call
//
// 工具设计原则：参数少、失败信息明确，让模型容易用对。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

// ---- JSON-RPC 基础 ----

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (a *adminServer) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Streamable HTTP 允许 GET 打开 SSE 流；我们只做请求-响应模式
		w.Header().Set("Allow", "POST")
		http.Error(w, "只支持 POST", http.StatusMethodNotAllowed)
		return
	}

	var req mcpRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32700, Message: "JSON 解析失败"}})
		return
	}

	// 通知（无 id）不需要响应
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "open-xiaoai", "version": "0.1.0"},
		}

	case "ping":
		resp.Result = map[string]any{}

	case "tools/list":
		resp.Result = map[string]any{"tools": mcpToolList()}

	case "tools/call":
		var call mcpToolCall
		if err := json.Unmarshal(req.Params, &call); err != nil {
			resp.Error = &mcpError{Code: -32602, Message: "参数格式错误"}
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		text, err := a.runMCPTool(ctx, call)
		if err != nil {
			resp.Result = map[string]any{
				"content": []any{map[string]any{"type": "text", "text": err.Error()}},
				"isError": true,
			}
			break
		}
		resp.Result = map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		}

	default:
		resp.Error = &mcpError{Code: -32601, Message: "不支持的方法: " + req.Method}
	}

	writeMCP(w, resp)
}

func writeMCP(w http.ResponseWriter, resp mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ---- 工具定义 ----

func mcpToolList() []mcpTool {
	return []mcpTool{
		{
			Name:        "play_music",
			Description: "在音箱上播放音乐。传 query 直接搜索并播放；或先 search_music 再传 index 播第 N 首。用户说“随便来点/随机放点/打乱”时把 shuffle 设为 true（没有指定具体某首歌时），会从搜索结果里随机挑一首起播，不会总是第一首。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "搜索关键词：歌名/歌手/组合/风格"},
					"index":   map[string]any{"type": "number", "description": "search_music 候选里的序号（从 1 开始）"},
					"shuffle": map[string]any{"type": "boolean", "description": "true=从搜索结果里随机挑一首起播（用户说随便来点/随机放点时用）；指定了具体歌名时不要用"},
				},
			},
		},
		{
			Name:        "search_music",
			Description: "只搜索不播放，返回前 5 首候选（序号/歌名/歌手）。用户说“找一下…”或需要确认时用，之后可用 play_music 的 index 播放。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "搜索关键词"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "play_playlist",
			Description: "播放网易云歌单。name 传歌单名（先 list_playlists 确认），「我喜欢」「收藏」会对应我喜欢的音乐。默认随机播放，不会每次都从第一首开始；用户明确要求按顺序播放时把 shuffle 设为 false。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    map[string]any{"type": "string", "description": "歌单名，例如：machine learning / 我喜欢的音乐"},
					"shuffle": map[string]any{"type": "boolean", "description": "是否随机播放，默认 true；用户说按顺序播放时传 false"},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "play_daily",
			Description: "播放网易云「每日推荐」（猜你喜欢），会自动从推荐里随机挑一首起播。用户说随便放点什么、放点推荐的、猜我喜欢、随机放点时用。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "list_playlists",
			Description: "列出网易云账号里的歌单（名称 + 歌曲数），不放。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "next_music",
			Description: "播放当前歌单的下一首。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "stop_music",
			Description: "停止音箱当前正在播放的音乐或语音。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "speak",
			Description: "让音箱说一段话（语音合成后播放）。适合播报结果、提醒，内容要短。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "description": "要朗读的文字，建议 60 字以内"},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "create_task",
			Description: "创建定时任务/提醒。到点后音箱会直接播报。普通提醒把要说的话写进 text；需要 AI 现查现答（天气、新闻等）时把 ai 设为 true，text 写成要求。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":     map[string]any{"type": "string", "description": "提醒内容，或给 AI 的要求"},
					"schedule": map[string]any{"type": "string", "description": "执行时间：once 2026-11-01 08:00 / daily 08:00 / weekly mon 08:00 / every 30m"},
					"ai":       map[string]any{"type": "boolean", "description": "true=到点让 AI 生成回答，false=直接播报 text"},
				},
				"required": []string{"text", "schedule"},
			},
		},
		{
			Name:        "list_tasks",
			Description: "列出当前所有定时任务（含下次执行时间）。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "update_task",
			Description: "修改已有任务的内容或执行时间（id 从 list_tasks 拿；只改一个就只传一个）。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":       map[string]any{"type": "string", "description": "任务 id"},
					"text":     map[string]any{"type": "string", "description": "新的播报内容/给 AI 的要求"},
					"schedule": map[string]any{"type": "string", "description": "新的时间，写法同 create_task"},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "set_task_enabled",
			Description: "暂停或恢复一个任务。enabled=false 暂停（到点不再播），true 恢复。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "string", "description": "任务 id"},
					"enabled": map[string]any{"type": "boolean", "description": "false=暂停，true=恢复"},
				},
				"required": []string{"id", "enabled"},
			},
		},
		{
			Name:        "run_task",
			Description: "立刻执行一次某个任务（不影响它原来的时间表）。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "任务 id"},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "stop_task",
			Description: "停止一个正在执行/正在播报的任务（打断这次执行，不改时间表）。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "任务 id"},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "delete_task",
			Description: "按 id 删除定时任务，id 从 list_tasks 里拿。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "任务 id，例如 t1a2b3c"},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "list_devices",
			Description: "列出家里的米家设备（名称、房间、在线状态）。控制前先用它确认设备名。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "set_volume",
			Description: "调整**音乐**音量。用户泛说“声音大点/小点/调小声音/静音”时默认指音乐，用这个；up/down 各调一档，mute 静音，unmute 取消静音。只在放音乐时有意义。若用户明确说“说话声/豆包声音/回复声音”，用 set_tts_volume。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{"type": "string", "description": "up / down / mute / unmute"},
				},
				"required": []string{"action"},
			},
		},
		{
			Name:        "set_tts_volume",
			Description: "调整**豆包说话声（TTS/回复语音）**的音量（0-100）。只有用户明确说“说话声/说话的声音/豆包声音/回复声音/TTS 音量”时才用；泛说“声音大点/小点”默认是音乐，用 set_volume。只影响 TTS 的 notify 通道，不改变音乐音量。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"volume": map[string]any{"type": "integer", "description": "0-100，0 为静音"},
				},
				"required": []string{"volume"},
			},
		},
		{
			Name:        "control_device",
			Description: "控制米家设备：开/关、亮度、色温、空调温度、模式。device 传设备名（可带房间，如「客厅灯」）。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"device": map[string]any{"type": "string", "description": "设备名，例如：客厅灯 / 主卧空调"},
					"action": map[string]any{"type": "string", "description": "on / off / toggle / brightness / color-temperature / temperature / mode / volume / mute / unmute / play / pause / next / previous"},
					"value":  map[string]any{"description": "数值（亮度 1-100、温度 16-30、模式编号等），on/off 不需要"},
				},
				"required": []string{"device", "action"},
			},
		},
		{
			Name:        "web_search",
			Description: "联网搜索最新信息（新闻、天气、股价、赛事、价格等）。优先走 DeepSeek 搜索，失败自动回落豆包。返回摘要，需要提炼成一两句口语。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "搜索关键词，越具体越好"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "get_time",
			Description: "获取当前日期和时间。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "end_conversation",
			Description: "结束当前对话：关闭连续对话窗口，回到只认唤醒词的状态。用户说「再见」「退下吧」「先这样」「不聊了」等要结束聊天的话时调用（回复照常说，说完就休眠）。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

// runMCPTool 执行工具，返回给模型看的文本。
func (a *adminServer) runMCPTool(ctx context.Context, call mcpToolCall) (string, error) {
	switch call.Name {
	case "play_music":
		q, _ := call.Arguments["query"].(string)
		q = strings.TrimSpace(q)
		if idx, ok := numArg(call.Arguments["index"]); ok && idx > 0 {
			return a.playSearchIndex(ctx, int(idx))
		}
		if q == "" {
			return "", fmt.Errorf("query 和 index 至少要有一个")
		}
		shuffle, _ := call.Arguments["shuffle"].(bool)
		return a.searchAndPlay(ctx, q, shuffle || wantsShuffle(q))

	case "search_music":
		q, _ := call.Arguments["query"].(string)
		q = strings.TrimSpace(q)
		if q == "" {
			return "", fmt.Errorf("query 不能为空")
		}
		songs, err := a.searchMusic(ctx, q, 8)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for i, s := range songs {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "%d. 《%s》- %s\n", i+1, s.Name, s.Artists)
		}
		return strings.TrimSpace(b.String()), nil

	case "play_playlist":
		name, _ := call.Arguments["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("name 不能为空")
		}
		shuffle := true
		if v, ok := call.Arguments["shuffle"].(bool); ok {
			shuffle = v
		}
		return a.playNamedPlaylist(ctx, name, shuffle)

	case "play_daily":
		return a.playDaily(ctx)

	case "list_playlists":
		client := a.neteaseClient()
		acc, err := client.GetAccount(ctx)
		if err != nil || acc == nil || !acc.LoggedIn {
			return "", fmt.Errorf("网易云未登录，请先在应用管理里扫码")
		}
		lists, err := client.UserPlaylists(ctx, acc.UserID)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, p := range lists {
			fmt.Fprintf(&b, "%s（%d 首）\n", p.Name, p.TrackCount)
		}
		return strings.TrimSpace(b.String()), nil

	case "next_music":
		return a.nextMusic(ctx)

	case "stop_music":
		if a.stopPlayback == nil {
			return "", fmt.Errorf("播放器未就绪")
		}
		a.stopPlayback()
		return "已停止播放", nil

	case "speak":
		t, _ := call.Arguments["text"].(string)
		t = strings.TrimSpace(t)
		if t == "" {
			return "", fmt.Errorf("text 不能为空")
		}
		if a.playTTS == nil {
			return "", fmt.Errorf("TTS 未就绪")
		}
		if err := a.playTTS(t); err != nil {
			return "", fmt.Errorf("播报失败: %w", err)
		}
		return "已播报", nil

	case "list_devices":
		if a.haClient() == nil {
			return "", fmt.Errorf("Home Assistant 未配置（在管理台「配置 → 家电」填 base_url 和 token）")
		}
		return a.toolHAListDevices(ctx)

	case "set_volume":
		action, _ := call.Arguments["action"].(string)
		switch action {
		case "up":
			before, after, err := a.speaker.AdjustMusicVolume(musicVolumeStep)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("音量 %d → %d", before, after), nil
		case "down":
			before, after, err := a.speaker.AdjustMusicVolume(-musicVolumeStep)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("音量 %d → %d", before, after), nil
		case "mute":
			v, acted, err := a.speaker.SetMusicMuted(true)
			if err != nil {
				return "", err
			}
			if !acted {
				return "已经是静音状态", nil
			}
			return fmt.Sprintf("已静音（原音量 %d）", v), nil
		case "unmute":
			v, acted, err := a.speaker.SetMusicMuted(false)
			if err != nil {
				return "", err
			}
			if !acted {
				return "本来就没静音", nil
			}
			return fmt.Sprintf("已取消静音，音量 %d", v), nil
		default:
			return "", fmt.Errorf("action 只能是 up/down/mute/unmute")
		}

	case "set_tts_volume":
		raw, ok := call.Arguments["volume"].(float64)
		if !ok {
			return "", fmt.Errorf("volume 必须是 0-100 的数字")
		}
		volume := int(raw)
		if volume < 0 || volume > 100 {
			return "", fmt.Errorf("volume 必须在 0-100 之间")
		}
		if err := a.speaker.SetTTSVolume(volume); err != nil {
			return "", err
		}
		return fmt.Sprintf("豆包 TTS 音量已设为 %d%%", volume), nil

	case "control_device":
		if a.haClient() == nil {
			return "", fmt.Errorf("Home Assistant 未配置（在管理台「配置 → 家电」填 base_url 和 token）")
		}
		device, _ := call.Arguments["device"].(string)
		action, _ := call.Arguments["action"].(string)
		msg, err := a.controlHA(ctx, deviceControlRequest{
			Device: device,
			Action: action,
			Value:  call.Arguments["value"],
		})
		if err != nil {
			return "", err
		}
		return msg, nil

	case "web_search":
		q, _ := call.Arguments["query"].(string)
		q = strings.TrimSpace(q)
		if q == "" {
			return "", fmt.Errorf("query 不能为空")
		}
		return a.toolWebSearch(ctx, q)

	case "create_task":
		if a.tasks == nil {
			return "", fmt.Errorf("定时任务未初始化")
		}
		text, _ := call.Arguments["text"].(string)
		schedule, _ := call.Arguments["schedule"].(string)
		ai, _ := call.Arguments["ai"].(bool)
		task, err := a.tasks.Add(text, schedule, ai)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已创建任务 %s：%s（%s）", task.ID, task.Text, DescribeSchedule(task)), nil

	case "list_tasks":
		if a.tasks == nil {
			return "", fmt.Errorf("定时任务未初始化")
		}
		list := a.tasks.List()
		if len(list) == 0 {
			return "现在没有任何定时任务", nil
		}
		var b strings.Builder
		for _, t := range list {
			state := "启用"
			if !t.Enabled {
				state = "已停用"
			}
			kind := "提醒"
			if t.AI {
				kind = "AI"
			}
			fmt.Fprintf(&b, "%s [%s/%s] %s（%s）\n", t.ID, kind, state, t.Text, DescribeSchedule(t))
		}
		return strings.TrimSpace(b.String()), nil

	case "update_task":
		if a.tasks == nil {
			return "", fmt.Errorf("定时任务未初始化")
		}
		id, _ := call.Arguments["id"].(string)
		text, _ := call.Arguments["text"].(string)
		schedule, _ := call.Arguments["schedule"].(string)
		task, err := a.tasks.Update(strings.TrimSpace(id), text, schedule)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已更新 %s：%s（%s）", task.ID, task.Text, DescribeSchedule(task)), nil

	case "set_task_enabled":
		if a.tasks == nil {
			return "", fmt.Errorf("定时任务未初始化")
		}
		id, _ := call.Arguments["id"].(string)
		enabled, _ := call.Arguments["enabled"].(bool)
		task, ok := a.tasks.SetEnabled(strings.TrimSpace(id), enabled)
		if !ok {
			return "", fmt.Errorf("没有这个任务: %s", id)
		}
		if enabled {
			return fmt.Sprintf("已恢复 %s（%s）", task.ID, DescribeSchedule(task)), nil
		}
		return "已暂停 " + task.ID, nil

	case "run_task":
		if a.runTask == nil {
			return "", fmt.Errorf("执行器未就绪")
		}
		id, _ := call.Arguments["id"].(string)
		if err := a.runTask(strings.TrimSpace(id)); err != nil {
			return "", err
		}
		return "已开始执行 " + id, nil

	case "stop_task":
		if a.stopTask == nil {
			return "", fmt.Errorf("执行器未就绪")
		}
		id, _ := call.Arguments["id"].(string)
		if !a.stopTask(strings.TrimSpace(id)) {
			return "", fmt.Errorf("任务 %s 当前没有在执行", id)
		}
		return "已停止 " + id, nil

	case "delete_task":
		if a.tasks == nil {
			return "", fmt.Errorf("定时任务未初始化")
		}
		id, _ := call.Arguments["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return "", fmt.Errorf("id 不能为空")
		}
		if !a.tasks.Delete(id) {
			return "", fmt.Errorf("没有这个任务: %s", id)
		}
		return "已删除 " + id, nil

	case "get_time":
		now := time.Now()
		wd := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[int(now.Weekday())]
		return now.Format("2006-01-02 15:04:05") + " " + wd, nil

	case "end_conversation":
		if a.engine == nil {
			return "", fmt.Errorf("引擎未就绪")
		}
		a.engine.EndConversation()
		return "已结束对话，回到唤醒监听", nil
	}
	return "", fmt.Errorf("未知工具: %s", call.Name)
}

// rankSongs 把更像"用户要的原曲"排在前面：歌名/歌手命中加分，翻唱伴奏减分。
func rankSongs(query string, songs []netease.Song) []netease.Song {
	q := strings.ToLower(strings.TrimSpace(query))
	tokens := strings.Fields(q)
	score := func(s netease.Song) int {
		name := strings.ToLower(s.Name)
		artist := strings.ToLower(s.Artists)
		sc := 0
		for _, t := range tokens {
			switch {
			case name == t:
				sc += 6
			case strings.Contains(name, t):
				sc += 2
			}
			if strings.Contains(artist, t) {
				sc += 4
			}
		}
		for _, bad := range []string{"翻唱", "cover", "伴奏", "纯音乐", "dj", "remix", "片段", "铃声", "女声版", "男声版"} {
			if strings.Contains(name, bad) || strings.Contains(artist, bad) {
				sc -= 8
			}
		}
		if s.Duration > 0 && s.Duration < 90_000 {
			sc -= 6 // 试听片段
		}
		return sc
	}
	out := append([]netease.Song(nil), songs...)
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	return out
}

// toolWebSearch 借豆包网页版的联网搜索回答问题。
// 用独立实例 + 新会话，避免污染语音对话的上下文。
func (a *adminServer) toolWebSearch(ctx context.Context, query string) (string, error) {
	// 1) 优先 DeepSeek 搜索接口
	if a.deepSeekAPIKey() != "" {
		results, err := a.deepSeekSearch(ctx, query)
		if err == nil && len(results) > 0 {
			log.Printf("🔎 搜索(DeepSeek): %q → %d 条", shortenText(query, 30), len(results))
			return formatSearchResults(results, 5), nil
		}
		if err != nil {
			log.Printf("⚠️ DeepSeek 搜索失败，回落豆包: %v", err)
		}
	}

	// 2) 回落豆包
	if a.getDoubaoWeb == nil {
		return "", fmt.Errorf("联网搜索未启用")
	}
	web := a.getDoubaoWeb()
	if web == nil {
		return "", fmt.Errorf("联网搜索未启用（豆包网页版未配置）")
	}
	if !web.LoggedIn() {
		return "", fmt.Errorf("豆包登录已失效，请到「应用管理」重新扫码")
	}
	tmp := NewDoubaoWeb(web.path)
	if !tmp.LoggedIn() {
		return "", fmt.Errorf("豆包登录读不到")
	}
	tmp.ResetConversation()
	ans, err := tmp.ChatStream(ctx, query, nil)
	if err != nil {
		return "", fmt.Errorf("搜索失败: %w", err)
	}
	if strings.TrimSpace(ans) == "" {
		return "", fmt.Errorf("搜索没有返回内容")
	}
	log.Printf("🔎 MCP 联网搜索: %q → %d 字", shortenText(query, 30), len([]rune(ans)))
	return ans, nil
}

// numArg 兼容模型传 number/string 两种情况。
func numArg(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}
