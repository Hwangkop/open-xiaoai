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
			Name: "list_books", Description: "列出可听的知识库书籍。用户说想听书但没选书、问有哪些书时先调用并简短介绍，不擅自选书。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name: "list_book_chapters", Description: "获取指定书的真实章节目录（不返回正文）。用户指定第一章等标题时先用本工具定位对应 chapter_id；目录第一个文件不一定是第一章。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"book": map[string]any{"type": "string", "description": "list_books 返回的书籍 ID 或书名"}}, "required": []string{"book"}},
		},
		{
			Name: "play_book", Description: "开始或续听书籍原文。服务直接读取知识库正文，用豆包声音分段合成、永久缓存、有限预取并自动续播，确认语播完才开始。不要输出书的正文、不要用 speak 或逐段调用工具代替听书。chapter_id 留空从书签续听，无书签从头；指定章节先查目录并传准确 ID。听书会替换当前音乐。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"book": map[string]any{"type": "string"}, "chapter_id": map[string]any{"type": "string", "description": "list_book_chapters 返回的章节 ID；续听时留空"}}, "required": []string{"book"}},
		},
		{
			Name: "control_book", Description: "暂停、继续、停止听书或切换相邻章节。停止保留音频和书签。服务重启后用 play_book 续听。用户在听书时说暂停/继续/下一章使用本工具，不能改用音乐或 speak 工具。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "stop", "next_chapter", "previous_chapter"}}}, "required": []string{"action"}},
		},
		{
			Name: "get_book_playback", Description: "读取实际听书状态、当前章节、分段、缓存准备状态和书签。不能凭对话记忆声称已经播放。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "play_music",
			Description: "单次点播一首音乐。传 query 搜索并选择一首，或先 search_music 再传 index 选择第 N 个候选。只播放选中的这一首，播完回到原来的推荐/我喜欢/歌单下一首；没有原歌单则停止。搜索候选不会变成持续歌单。shuffle=true 仅随机选择一首；持续播放请用 play_playlist 或 play_daily。",
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
			Description: "只搜索不播放，也不改变当前歌单，返回前 5 首候选（序号/歌名/歌手）。之后可用 play_music 的 index 单次点播选中的一首。",
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
			Name:        "search_bilibili_music",
			Description: "搜索 B 站公开视频里的音乐音频，返回标题、UP 主、时长和 bvid。用户明确说从 B 站/Bilibili 找歌、听视频音频时使用。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "歌曲名、歌手或音乐主题"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "play_bilibili_music",
			Description: "播放用户选定的 B 站视频或分 P，也可传用户提供的视频链接。建立单项 B 站队列，播完停止，不跳回网易云。后台准备音轨，工具返回仅代表已受理，不能说已经出声；长视频可能准备较久。整个合集使用 play_bilibili_playlist。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bvid": map[string]any{"type": "string", "description": "BV 开头的视频 ID"},
					"cid":  map[string]any{"type": "integer", "description": "可选，多 P 视频的分 P ID；不填使用第一 P"},
				},
				"required": []string{"bvid"},
			},
		},
		{
			Name:        "download_bilibili_audio",
			Description: "把 search_bilibili_music 返回的 B 站公开视频音轨永久保存到本地音乐目录；有 Hi-Res 无损时保存 FLAC，否则保存 320kbps MP3。只有用户明确要求保存/下载时才调用。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bvid": map[string]any{"type": "string", "description": "BV 开头的视频 ID"},
					"cid":  map[string]any{"type": "integer", "description": "可选，多 P 视频的分 P ID；不填使用第一 P"},
				},
				"required": []string{"bvid"},
			},
		},
		{
			Name:        "get_bilibili_catalog",
			Description: "识别 B 站视频链接或 BV 号，读取所属合集和分 P 目录。合集是多个视频，分 P 是当前视频的各部分；长视频不等于单曲，没有时间点不能声称可按歌曲切换。返回分页目录元信息，不下载、不播放。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"reference": map[string]any{"type": "string", "description": "用户提供的视频链接或 BV 号"},
				"offset":    map[string]any{"type": "integer", "minimum": 0, "description": "目录偏移，默认 0"},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "每页数量，默认 20"},
			}, "required": []string{"reference"}},
		},
		{
			Name:        "play_bilibili_playlist",
			Description: "按用户明确要求播放 B 站完整合集或当前视频全部分 P，先用 get_bilibili_catalog 确认目录；不能因搜索命中一个视频就擅自播放所属合集。共用下一首、暂停、继续、停止控制；下一首切换视频或分 P，不保证切到长视频内的下一首歌曲。后台按项准备并缓存，未就绪前只说正在准备。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"reference":   map[string]any{"type": "string", "description": "目录对应的视频链接或 BV 号"},
				"kind":        map[string]any{"type": "string", "enum": []string{"collection", "parts"}},
				"start_index": map[string]any{"type": "integer", "minimum": 1, "description": "从第几项起播，从 1 开始，默认 1"},
				"shuffle":     map[string]any{"type": "boolean", "description": "明确要求随机播放时为 true，忽略起始序号"},
			}, "required": []string{"reference", "kind"}},
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
			Description: "推进当前来源的持续队列。B 站切换到下一视频或分 P，不跳回网易云；网易云单次点播则回到原歌单的下一首，不会顺着搜索候选播放。",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "control_music",
			Description: "暂停、继续、上一项或重播当前音乐队列，自动作用于当前网易云或 B 站来源。准备 B 站音轨时暂停会撤销该次起播，继续时重试当前项。听书使用 control_book。",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "previous", "replay"}},
			}, "required": []string{"action"}},
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
	case "list_books", "list_book_chapters", "play_book", "control_book", "get_book_playback":
		return a.runAudiobookTool(call)
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

	case "search_bilibili_music":
		if a.bilibili == nil {
			return "", fmt.Errorf("B 站音乐未启用")
		}
		q, _ := call.Arguments["query"].(string)
		q = strings.TrimSpace(q)
		if q == "" {
			return "", fmt.Errorf("query 不能为空")
		}
		items, err := a.bilibili.search(ctx, q)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "B 站没有搜到匹配视频", nil
		}
		var b strings.Builder
		for i, item := range items {
			if i >= 5 {
				break
			}
			recommended := ""
			if i == 0 {
				recommended = "，推荐"
			}
			fmt.Fprintf(&b, "%d. %s - %s（%s，%s%s）\n", i+1, item.Title, item.Author, item.Duration, item.BVID, recommended)
		}
		return strings.TrimSpace(b.String()), nil

	case "play_bilibili_music":
		if a.bilibili == nil {
			return "", fmt.Errorf("B 站音乐未启用")
		}
		bvid, _ := call.Arguments["bvid"].(string)
		bvid = strings.TrimSpace(bvid)
		cid := int64(0)
		if value, ok := numArg(call.Arguments["cid"]); ok {
			cid = int64(value)
		}
		return a.playBilibiliVideo(ctx, bvid, cid)

	case "get_bilibili_catalog":
		if a.bilibili == nil {
			return "", fmt.Errorf("B 站音乐未启用")
		}
		reference, _ := call.Arguments["reference"].(string)
		catalog, err := a.bilibili.catalog(ctx, reference)
		if err != nil {
			return "", err
		}
		offset, limit := 0, 20
		if n, ok := numArg(call.Arguments["offset"]); ok {
			offset = int(n)
		}
		if n, ok := numArg(call.Arguments["limit"]); ok {
			limit = int(n)
		}
		if offset < 0 || limit < 1 || limit > 50 {
			return "", fmt.Errorf("目录分页参数无效")
		}
		clip := func(items []bilibiliCatalogItem) []bilibiliCatalogItem {
			start := min(offset, len(items))
			return items[start:min(start+limit, len(items))]
		}
		out := map[string]any{"video": catalog.Video, "selected_cid": catalog.SelectedCID, "parts_total": len(catalog.Parts), "parts": clip(catalog.Parts), "offset": offset, "limit": limit}
		if c := catalog.Collection; c != nil {
			out["collection"] = map[string]any{"id": c.ID, "title": c.Title, "total": c.Total, "items": clip(c.Items)}
		}
		data, err := json.Marshal(out)
		return string(data), err

	case "play_bilibili_playlist":
		reference, _ := call.Arguments["reference"].(string)
		kind, _ := call.Arguments["kind"].(string)
		index := 1
		if n, ok := numArg(call.Arguments["start_index"]); ok {
			index = int(n)
		}
		shuffle, _ := call.Arguments["shuffle"].(bool)
		return a.playBilibiliPlaylist(ctx, reference, kind, index-1, shuffle)

	case "download_bilibili_audio":
		if a.bilibili == nil {
			return "", fmt.Errorf("B 站音乐未启用")
		}
		bvid, _ := call.Arguments["bvid"].(string)
		bvid = strings.TrimSpace(bvid)
		cid := int64(0)
		if value, ok := numArg(call.Arguments["cid"]); ok {
			cid = int64(value)
		}
		cached, err := a.bilibili.prepare(ctx, bvid, cid)
		if err != nil {
			return "", err
		}
		_, savedName, err := a.bilibili.saveToMusic(cached)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已永久保存《%s》到音乐目录：%s", cached.Video.Title, savedName), nil

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
	case "control_music":
		action, _ := call.Arguments["action"].(string)
		switch action {
		case "pause", "resume", "previous", "replay":
		default:
			return "", fmt.Errorf("不支持的音乐控制")
		}
		if err := a.controlQueuedMusic(ctx, action, nil); err != nil {
			return "", err
		}
		return "音乐控制指令已执行。", nil

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
