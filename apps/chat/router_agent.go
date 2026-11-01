package main

// 音乐态的第 4 道闸：语义路由。
//
// 前面三道（白名单 / 唤醒词 / 歌词比对）都是确定性的，剩下的才交给模型判断：
// 用户到底是想切歌、调音量、换内容，还是在跟音箱聊天。模型只输出 JSON，
// 不生成自然语言；置信度不够就当噪声丢掉，绝不"猜着执行"。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

type musicIntent string

const (
	musicIntentNext   musicIntent = "next"
	musicIntentStop   musicIntent = "stop"
	musicIntentVolUp  musicIntent = "volume_up"
	musicIntentVolDn  musicIntent = "volume_down"
	musicIntentMute   musicIntent = "mute"
	musicIntentUnmute musicIntent = "unmute"
	musicIntentPlay   musicIntent = "play"
	musicIntentDialog musicIntent = "dialog"
	musicIntentIgnore musicIntent = "ignore"
)

type musicDecision struct {
	Intent     string  `json:"intent"`
	Query      string  `json:"query"`
	Confidence float64 `json:"confidence"`
}

const musicRouterSystemPrompt = `你是智能音箱的意图路由器。用户在放音乐时对音箱说了一句话，你只判断他想干什么。不要聊天、不要解释、不要输出 JSON 以外的任何内容。

输出格式（只输出一个 JSON 对象）：
{"intent": "...", "query": "...", "confidence": 0.0}

intent 只能取这些值：
- "next"        切歌、不想听这首、换一首
- "stop"        停止播放、关掉音乐
- "volume_up"   声音大一点、太小声听不清
- "volume_down" 声音小一点、太吵
- "mute"        静音、别出声
- "unmute"      取消静音、打开声音
- "play"        想听别的：query 填歌名/歌手/歌单/风格
- "dialog"      在跟音箱聊天、提问、需要想一想再回答
- "ignore"      歌词、噪声、自言自语、明显不是在跟音箱说话

判断原则：
1. 拿不准就判 "ignore"，confidence 给 0.3 以下。
2. 歌词经常写成命令句（"留下来""别走""告诉我"），不要因为像命令就判操作；只有明确指向音箱功能才算操作。
3. 既有情绪又有操作（"太吵了换一首"）按操作判。
4. query 只填用户明确说出来的内容，没有就留空字符串。`

// routeMusicText 把音乐态下没被前面几道闸拦住的话交给路由器。
// fromWake=true 表示这句话带唤醒词（"豆包豆包，换一首"），本地词面闸不再适用。
// 始终返回 true（表示"已接住"），真正的判断在后台完成，避免阻塞 ASR 线程。
func (e *Engine) routeMusicText(text string, via string) bool {
	cfg := e.Config()
	if cfg == nil || !cfg.MusicRouter.Enabled {
		return false
	}
	if e.codexRouter == nil && e.client == nil {
		return false
	}
	song, artists, volume := "", "", 0
	if e.musicDesc != nil {
		song, artists, volume = e.musicDesc()
	}
	timeout := time.Duration(cfg.MusicRouter.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	go func() {
		// 路由期间没有 AI 回复，点歌必须立刻起播（状态由 Speaker 持有）
		e.speaker.BeginImmediatePlay()
		defer e.speaker.EndImmediatePlay()
		// 这一轮最长几秒，灯也要亮着，否则用户以为没听见
		e.speaker.ShowThinkingLED()
		defer e.speaker.HideThinkingLED()
		start := time.Now()

		// 第一步：进程内工具路由（1 次 function calling，能调全部 MCP 工具，约 1 秒）。
		// 这就是"常用指令也能调 MCP"的那条路——不需要为每个能力写分发分支。
		if e.toolRun != nil && cfg.MusicRouter.Engine != "codex" {
			tctx, tcancel := context.WithTimeout(context.Background(), 6*time.Second)
			action, say, terr := e.decideWithTools(tctx, text, e.routeState())
			tcancel()
			if terr == nil {
				e.applyCodexRoute(context.Background(), text, action, via, time.Since(start).Milliseconds(), say)
				return
			}
			log.Printf("🧭 工具路由失败，回退分类器 %q: %v", text, terr)
		}

		// 第二步（兜底）：轻量分类器，只回固定 intent，由本地执行。
		fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
		d, err := e.decideMusicIntent(fctx, text, song, artists, volume)
		fcancel()
		if err == nil && !(cfg.MusicRouter.Engine == "codex" && d.Intent == string(musicIntentDialog)) {
			e.applyMusicDecision(context.Background(), text, d, time.Since(start).Milliseconds(), via)
			return
		}

		// 分类器失败（或显式配置 engine=codex 且判不准）：交给 codex 路由线程，
		// 它有完整工具集，能处理分类器覆盖不到的操作。
		if e.codexRouter != nil {
			ctx, cancel := context.WithTimeout(context.Background(), routeTimeout)
			defer cancel()
			if err != nil {
				log.Printf("🧭 分类器失败，升级到 codex 路由 %q: %v", text, err)
			}
			action, say, rerr := e.codexRouter.Route(ctx, text, e.routeState())
			if rerr == nil {
				e.applyCodexRoute(ctx, text, action, via, time.Since(start).Milliseconds(), say)
				return
			}
			log.Printf("🧭 codex 路由也失败，忽略 %q: %v", text, rerr)
			recordRoute(routeEntry{
				Text: text, Stage: "router", Result: "error",
				LatencyMs: time.Since(start).Milliseconds(),
			})
			return
		}

		if err != nil {
			log.Printf("🧭 路由失败，忽略 %q: %v", text, err)
			recordRoute(routeEntry{Text: text, Stage: "router", Result: "error", LatencyMs: time.Since(start).Milliseconds()})
		}
	}()
	return true
}

func (e *Engine) decideMusicIntent(ctx context.Context, text, song, artists string, volume int) (musicDecision, error) {
	cfg := e.Config()
	model := strings.TrimSpace(cfg.MusicRouter.Model)
	if model == "" {
		model = cfg.LLM.Model
	}
	user := fmt.Sprintf("当前在放《%s》- %s，音量 %d/100。用户说：%s", song, artists, volume, text)
	req := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: musicRouterSystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: user},
		},
		Temperature: 0,
		MaxTokens:   400, // 推理模型的思考也吃 token；太小会导致 content 为空
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	}
	resp, err := e.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return musicDecision{}, err
	}
	if len(resp.Choices) == 0 {
		return musicDecision{}, fmt.Errorf("空响应")
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")

	var d musicDecision
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &d); err != nil {
		return musicDecision{}, fmt.Errorf("解析 JSON 失败: %w (%s)", err, shortenText(raw, 80))
	}
	d.Intent = strings.ToLower(strings.TrimSpace(d.Intent))
	d.Query = strings.TrimSpace(d.Query)
	return d, nil
}

// applyMusicDecision 执行路由结果。置信度不够就静默，避免"猜着"动手。
func (e *Engine) applyMusicDecision(ctx context.Context, text string, d musicDecision, latencyMs int64, via string) {
	minConf := 0.6
	if cfg := e.Config(); cfg != nil && cfg.MusicRouter.MinConfidence > 0 {
		minConf = cfg.MusicRouter.MinConfidence
	}
	log.Printf("🧭 路由: intent=%s conf=%.2f query=%q text=%q", d.Intent, d.Confidence, d.Query, text)

	entry := routeEntry{
		Text: text, Stage: "router", Via: via, Action: d.Intent, Query: d.Query,
		Confidence: d.Confidence, LatencyMs: latencyMs,
	}
	record := func(result string) {
		entry.Result = result
		recordRoute(entry)
	}

	intent := musicIntent(d.Intent)
	if intent == musicIntentIgnore || intent == "" {
		record("ignored")
		return
	}
	if d.Confidence < minConf {
		log.Printf("🧭 置信度 %.2f < %.2f，忽略", d.Confidence, minConf)
		record("low_confidence")
		return
	}

	switch intent {
	case musicIntentNext:
		if e.musicNext == nil {
			record("no_tool")
			return
		}
		if msg, err := e.musicNext(ctx); err == nil {
			log.Printf("🎵 %s", msg)
			record("executed")
		} else {
			log.Printf("⚠️ 下一首失败: %v", err)
			record("error")
		}
	case musicIntentStop:
		if e.musicStop != nil {
			e.musicStop()
			record("executed")
			return
		}
		record("no_tool")
	case musicIntentVolUp:
		e.adjustMusicVolume(musicVolumeStep)
		record("executed")
	case musicIntentVolDn:
		e.adjustMusicVolume(-musicVolumeStep)
		record("executed")
	case musicIntentMute:
		e.setMusicMuted(true)
		record("executed")
	case musicIntentUnmute:
		e.setMusicMuted(false)
		record("executed")
	case musicIntentPlay:
		if d.Query == "" || e.musicPlayQuery == nil {
			log.Printf("🧭 play 意图但缺少 query 或播放器未就绪")
			record("no_tool")
			return
		}
		if msg, err := e.musicPlayQuery(ctx, d.Query); err == nil {
			log.Printf("🎵 %s", msg)
			record("executed")
			// 点歌要有一句口头确认：放错了（比如"如愿"被听成"无愿"）能立刻听出来。
			// 播报会短暂抢占音乐，播完由 Speaker 自动接回去。
			if say := strings.TrimSpace(msg); say != "" && len([]rune(say)) <= 30 {
				go func() {
					if err := e.speaker.PlayTTS(say, true); err != nil {
						log.Printf("⚠️ 点歌确认播报失败: %v", err)
					}
				}()
			}
		} else {
			log.Printf("⚠️ 播放 %q 失败: %v", d.Query, err)
			record("error")
		}
	case musicIntentDialog:
		// 交给主聊天链路：文本已经有了，不必再走一遍对话 ASR
		record("dialog")
		e.handleDialogTurn(text, false, UtteranceASR{})
	default:
		record("unknown")
	}
}
