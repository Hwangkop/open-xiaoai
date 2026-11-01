package main

// 进程内工具路由：一次 DeepSeek function calling 调用，直接调用我们自己的 21 个 MCP 工具。
//
// 和"固定 9 个 intent 的分类器"相比，它不需要为每个新能力写 Go 分发分支——
// 工具清单就是 mcpToolList()，执行就是 a.runMCPTool()。
// 也不用起 codex 路由线程（那要 4 秒），一轮约 1 秒。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

const toolRouterPrompt = `你是智能音箱的指令执行器。能用工具办就直接调工具，不要解释、不要闲聊。

规则：
1. 明确的指令（放歌/切歌/停/音量/开关灯/设提醒/查天气…）→ 直接调对应工具
2. 需要组织语言回答、闲聊、追问 → 不要调工具，只输出 {"action":"dialog"}
3. 歌词、电视声、噪声、明显不是跟音箱说话 → 不调工具，只输出 {"action":"ignore"}
4. 调完工具不要总结，工具返回的文本会直接被播报出去
5. 用户说“随机”“随便放点”“打乱”时属于随机播放：放推荐用 play_daily；放歌单用 play_playlist（shuffle=true）；按歌手/风格点歌用 play_music（shuffle=true）。不要默认放列表第一首。

一次只调必要的工具，不要连环调用。`

// openAIToolsFromMCP 把 MCP 的工具定义转成 OpenAI function calling 的 tools 参数。
func openAIToolsFromMCP(tools []mcpTool) []openai.Tool {
	out := make([]openai.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}

// decideWithTools 让模型在工具清单里选，选完直接执行。
// 返回 action（done/dialog/ignore）和需要播报的确认语。
func (e *Engine) decideWithTools(ctx context.Context, text, state string) (string, string, error) {
	action, say, _, _, err := e.toolRoute(ctx, text, state, true)
	return action, say, err
}

// previewToolRoute 只问模型、不执行工具（调试用）：返回它想调什么、原始输出是什么。
func (e *Engine) previewToolRoute(ctx context.Context, text, state string) ([]mcpToolCall, string, error) {
	_, _, calls, content, err := e.toolRoute(ctx, text, state, false)
	return calls, content, err
}

// toolRoute 工具路由的实体：execute=false 时只返回模型的意图，不落副作用。
func (e *Engine) toolRoute(ctx context.Context, text, state string, execute bool) (string, string, []mcpToolCall, string, error) {
	cfg := e.Config()
	if cfg == nil || e.client == nil || e.toolRun == nil || e.toolDefs == nil {
		return "", "", nil, "", fmt.Errorf("工具路由未就绪")
	}
	model := strings.TrimSpace(cfg.MusicRouter.Model)
	if model == "" {
		model = cfg.LLM.Model
	}
	user := text
	if state != "" {
		user = state + "\n用户说：" + text
	}

	req := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: toolRouterPrompt},
			{Role: openai.ChatMessageRoleUser, Content: user},
		},
		Tools:       e.toolDefs(),
		Temperature: 0,
		MaxTokens:   400,
	}
	resp, err := e.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", "", nil, "", err
	}
	if len(resp.Choices) == 0 {
		return "", "", nil, "", fmt.Errorf("空响应")
	}
	msg := resp.Choices[0].Message

	if len(msg.ToolCalls) == 0 {
		action, say := parseRouterAction(msg.Content)
		// 没调工具也要留痕：否则用户只看到"啥也没干"，不知道模型判成了什么
		log.Printf("🧭 工具路由: 无工具调用 → %s（原始输出 %q）", action, shortenText(strings.TrimSpace(msg.Content), 120))
		return action, say, nil, msg.Content, nil
	}
	// 把模型的选择原样记下来，排查"为什么没执行"时直接看这行
	var calls []mcpToolCall
	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
			_ = json.Unmarshal([]byte(s), &args)
		}
		calls = append(calls, mcpToolCall{Name: tc.Function.Name, Arguments: args})
		log.Printf("🧭 工具路由选择: %s(%s)", tc.Function.Name, shortenText(tc.Function.Arguments, 120))
	}
	if !execute {
		return "done", "", calls, msg.Content, nil
	}

	var results []string
	say := ""
	for _, call := range calls {
		out, err := e.toolRun(ctx, call.Name, call.Arguments)
		if err != nil {
			log.Printf("⚠️ 工具路由执行 %s 失败: %v", call.Name, err)
			return "done", "", calls, msg.Content, nil
		}
		log.Printf("🧭 工具路由执行: %s(%v) → %s", call.Name, call.Arguments, shortenText(out, 40))
		out = strings.TrimSpace(out)
		// 只有"点歌"才需要口头确认（用户看不见放了哪首）；
		// 调音量、开关灯这类本来就有即时反馈，不再多嘴。
		if say == "" && strings.Contains(out, "正在播放") {
			say = out
		}
		results = append(results, out)
	}
	if say != "" && len([]rune(say)) > 40 {
		say = string([]rune(say)[:40])
	}
	return "done", say, calls, msg.Content, nil
}
