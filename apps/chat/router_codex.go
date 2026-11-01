package main

// 音乐态路由器（codex 版）。
//
// 和原来的 HTTP 分类器相比，它跑在同一个 codex app-server 的**另一条 thread** 上：
// 用 thread/start 的 config 覆盖把可用工具收窄到音乐/音量那几个
// （mcp_servers.<server>.enabled_tools），既能真的调工具办事，又不会越界去开灯、建任务。
// 这套机制抄自 work-connect 的 buildCodexThreadConfigOverrides（codex -c 点分覆盖）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// defaultCodexRouterPrompt 默认提示词；可以在 config.yaml 的 music_router.prompt 里覆盖。
const defaultCodexRouterPrompt = `你是智能音箱的指令执行器。用户对音箱说了一句话，你只负责**办事或转交**，不要陪聊。

你能用全部工具（音乐、音量、家电、定时任务、搜索、播报…），和主助手完全一样。

判断规则：
1. 是明确的指令（放歌/切歌/停/音量/开关灯/设提醒/查天气…）→ **直接调工具办掉**
2. 需要解释、闲聊、追问、或者要组织语言回答 → **不要调工具**，转给主助手
3. 歌词、电视声、噪声、明显不是对音箱说的 → 不要调工具
4. 拿不准 → 当成 3

**歌名经常被语音识别听错（同音/近音字）**，比如「如愿」被听成「无愿」、「稻香」听成「道香」。所以：
5. 点歌时如果这个歌名不常见、或者搜出来的结果一看就不对（歌名/歌手都陌生），先用 search_music 看候选，别直接放
6. 候选里如果有同音/近音但明显更主流的歌，放那首；一个都不像就转 dialog，让主助手复述一句确认
7. 用户说「换一首 X」时 X 是歌名，不是要切下一首——除非他没给歌名

每次回复的最后一行只输出一个 JSON，不要写别的话：
- 已经办完：{"action":"done"}
- 已办完且需要口头确认（比如点歌，要让人听出放了哪首）：{"action":"done","say":"好，放《歌名》"}
- 交给主助手回答：{"action":"dialog"}
- 忽略：{"action":"ignore"}

say 只在"用户看不见结果"时用（点歌、换歌单），不超过 20 字；切歌、调音量这类本来就能听出来的不要加。

能一次办完就一次办完，不要连环调用工具，也不要解释你做了什么。`

// codexRouter 复用主 app-server，但用独立 thread + 受限工具集。
type codexRouter struct {
	mu       sync.Mutex
	srv      *CodexAppServer
	thread   string
	promptFn func() string // 提示词可以走配置；改完需要"新开会话"才生效
}

func newCodexRouter(srv *CodexAppServer, promptFn func() string) *codexRouter {
	return &codexRouter{srv: srv, promptFn: promptFn}
}

func (r *codexRouter) prompt() string {
	if r.promptFn != nil {
		if p := strings.TrimSpace(r.promptFn()); p != "" {
			return p
		}
	}
	return defaultCodexRouterPrompt
}

// threadID 懒创建路由会话。
func (r *codexRouter) threadID(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.thread != "" {
		return r.thread, nil
	}
	if err := r.srv.Start(ctx); err != nil {
		return "", err
	}
	// 工具集和主对话完全一致（不设 enabled_tools，继承 config.toml 里的 MCP 配置）；
	// 区别只在提示词：办事 + 判断要不要转给主助手，不陪聊。
	id, err := r.srv.StartThreadWithOptions(ctx, r.prompt(), ThreadOptions{
		Cwd: r.srv.cfg.Workspace,
	})
	if err != nil {
		return "", err
	}
	r.thread = id
	log.Printf("🧭 路由会话已建立（thread=%s，工具同主对话）", id)
	return id, nil
}

// CurrentThread 返回路由会话 id（可能为空）。
func (r *codexRouter) CurrentThread() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.thread
}

// Reset 丢掉路由会话，下次 Route 会重新建。
func (r *codexRouter) Reset() { r.resetThread() }

// resetThread 丢掉缓存的路由会话，下次 Route 会重新建。
func (r *codexRouter) resetThread() {
	r.mu.Lock()
	r.thread = ""
	r.mu.Unlock()
}

// Route 让路由 agent 处理一句话，返回 done / dialog / ignore。
func (r *codexRouter) Route(ctx context.Context, text, state string) (string, string, error) {
	id, err := r.threadID(ctx)
	if err != nil {
		return "", "", err
	}
	prompt := text
	if state != "" {
		prompt = state + "\n用户说：" + text
	}

	var mu sync.Mutex
	var sb strings.Builder
	reply, err := r.srv.TurnOnOpts(ctx, id, prompt, func(delta string) {
		mu.Lock()
		sb.WriteString(delta)
		mu.Unlock()
	}, nil)
	if err != nil {
		// 出错必须往上抛：调用方要能回退到分类器，不能把失败当成 ignore 吞掉。
		// 同时丢掉缓存的 thread：app-server 重启（turn 超时/部署）后旧 id 已经不存在，
		// 不清理的话路由会一直失败，直到整个进程重启。
		r.resetThread()
		return "", "", err
	}
	if strings.TrimSpace(reply) == "" {
		mu.Lock()
		reply = sb.String()
		mu.Unlock()
	}
	if strings.TrimSpace(reply) == "" {
		// 空回复不能当成"忽略"——那会把真实指令静默丢掉，也让用户以为没反应。
		// 抛错让调用方回退到分类器。
		return "", "", fmt.Errorf("路由没有返回内容")
	}
	action, say := parseRouterAction(reply)
	return action, say, nil
}

// parseRouterAction 从回复里取最后一个 {"action":...,"say":...}。
func parseRouterAction(reply string) (action, say string) {
	idx := strings.LastIndex(reply, "{")
	if idx < 0 {
		return "ignore", ""
	}
	var v struct {
		Action string `json:"action"`
		Say    string `json:"say"`
	}
	if err := json.Unmarshal([]byte(reply[idx:]), &v); err != nil {
		// 可能后面还跟了别的字符，截到最后 } 再试一次
		end := strings.LastIndex(reply, "}")
		if end > idx {
			if err2 := json.Unmarshal([]byte(reply[idx:end+1]), &v); err2 != nil {
				return "ignore", ""
			}
		} else {
			return "ignore", ""
		}
	}
	act := strings.ToLower(strings.TrimSpace(v.Action))
	switch act {
	case "done", "dialog", "ignore":
		return act, strings.TrimSpace(v.Say)
	}
	return "ignore", ""
}

// routeTimeout 路由一轮的上限（含工具调用）。
const routeTimeout = 20 * time.Second

// applyCodexRoute 处理 codex 路由的结果：动作已经由工具执行过了，这里只决定要不要转对话。
func (e *Engine) applyCodexRoute(ctx context.Context, text, action, via string, latencyMs int64, say string) {
	entry := routeEntry{Text: text, Stage: "router", Via: via, Action: action, LatencyMs: latencyMs}
	switch action {
	case "dialog":
		entry.Result = "dialog"
		recordRoute(entry)
		e.handleDialogTurn(text, false, UtteranceASR{})
	case "done":
		entry.Result = "executed"
		recordRoute(entry)
		// 路由器说"办完了，而且要口头确认"（点歌这类看不见结果的）
		if say = strings.TrimSpace(say); say != "" && len([]rune(say)) <= 40 {
			go func() {
				if err := e.speaker.PlayTTS(say, true); err != nil {
					log.Printf("⚠️ 确认播报失败: %v", err)
				}
			}()
		}
	default:
		entry.Result = "ignored"
		recordRoute(entry)
	}
}

// routeState 给路由 agent 的上下文（当前在放什么、音量多少）。
func (e *Engine) routeState() string {
	if e.musicDesc == nil {
		return ""
	}
	song, artists, volume := e.musicDesc()
	if song == "" && volume == 0 {
		return ""
	}
	return fmt.Sprintf("当前在放《%s》- %s，音量 %d/100。", song, artists, volume)
}
