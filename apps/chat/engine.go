package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	openai "github.com/sashabaranov/go-openai"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
)

// Engine handles user messages: keyword matching → OpenAI streaming → sentence-by-sentence TTS.
type Engine struct {
	configMu sync.RWMutex
	config   *AppConfig
	speaker  *Speaker
	client   *openai.Client
	// doubaoWeb 非空且已登录时，优先走豆包网页直连（不经过 openai 客户端）
	doubaoWeb *DoubaoWeb

	mu         sync.Mutex
	cancelFunc context.CancelFunc
	lastMsgTS  int64

	// 同一句话可能同时被小爱 ASR 和本地上行 ASR 听到，去重避免问豆包两次
	turnMu   sync.Mutex
	turnText string
	turnAt   time.Time

	historyMu sync.Mutex
	history   []openai.ChatCompletionMessage

	// 小爱是否正在播报（从 instruction.log 的 Speak/FinishSpeakStream 推出来）
	xiaoiSpeaking   atomic.Bool
	xiaoiSpeakUntil atomic.Int64

	// localASRDisabled 管理台实验开关：关闭后服务端不再处理 mic 流和本地 kws，
	// 原生小爱 ASR（instruction）不受影响。用于隔离本地 ASR 对音频测试的干扰。
	localASRDisabled atomic.Bool

	// nativeTurn 标记本轮由「小爱同学」触发：这种轮次结束后不续开本地对话窗口，
	// 否则本地 ASR 会把小爱的播报、环境声继续当成指令送给豆包。
	nativeTurn atomic.Bool
	// nativeChain 连续的原生轮数：回答完自动重开一次原生收音，实现连续对话；
	// 达到上限就不再自动续，需要重新喊「小爱同学」。
	nativeChain atomic.Int32
	// turnSeq 回复轮次。旧轮次被新轮次打断后，旧协程不能再去解冻/续听，
	// 否则会把新轮次的接管提前结束，让小爱 TTS 漏出来。
	turnSeq atomic.Uint64

	// codex 非空且 provider=codex 时，走 Codex CLI（一次性 exec）
	codex *CodexClient
	// codexServer 非空时优先用它（常驻 app-server，省掉冷启动）
	codexServer *CodexAppServer

	// 音乐态下的语音控制（由 runtime 注入到管理端实现）
	musicNext func(context.Context) (string, error)
	musicStop func()

	// 音乐态语义路由的依赖（由管理端注入）
	musicDesc      func() (song, artists string, volume int)
	musicPlayQuery func(ctx context.Context, query string, shuffle bool) (string, error)

	// codexRouter 非空时，音乐态路由可以升级到 codex 的独立 thread（完整工具集）
	codexRouter *codexRouter

	// 进程内工具路由的依赖（由管理端注入）：工具清单 + 执行器。
	// 有了它，路由就能直接调用全部 MCP 工具，不必为每个能力写分发分支。
	toolDefs func() []openai.Tool
	toolRun  func(ctx context.Context, name string, args map[string]any) (string, error)

	// wakeStore 用户自定义的唤醒词（管理台可编辑）
	wakeStore *wakeStore

	// armedUntil 只喊触发词后进入的"对话模式"截止时间（unix ms）；0 表示未激活。
	// 激活后下一句不管带不带触发词都直接交给 AI，避免"豆包豆包"被当成问题发出去。
	armedUntil atomic.Int64

	// endAfterReply 本轮回复结束后不再自动续窗（MCP 工具 end_conversation 触发）。
	endAfterReply atomic.Bool
}

// dialogArmWindow 只喊触发词后，等待下一句的时长。
const dialogArmWindow = 15 * time.Second

// defaultContinueSeconds AI 回答结束后自动续开的对话窗口默认值（秒）。
// 可用 config 的 continuation_seconds 覆盖。
const defaultContinueSeconds = 60

// exitWords 命中即结束对话（不再续窗）。
var exitWords = []string{"拜拜", "再见", "没事了", "不用了", "结束对话", "不聊了"}

func NewEngine(cfg *AppConfig, speaker *Speaker) *Engine {
	llm := cfg.GetLLM()
	clientCfg := openai.DefaultConfig(llm.APIKey)
	clientCfg.BaseURL = llm.BaseURL

	if cfg.Proxy != "" {
		proxyURL, err := url.Parse(cfg.Proxy)
		if err != nil {
			log.Printf("⚠️ 无效的代理地址 %q: %v，将不使用代理", cfg.Proxy, err)
		} else {
			clientCfg.HTTPClient = &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(proxyURL),
				},
			}
			log.Printf("🔗 使用代理: %s", cfg.Proxy)
		}
	}

	return &Engine{
		config:  cfg,
		speaker: speaker,
		client:  openai.NewClientWithConfig(clientCfg),
	}
}

func (e *Engine) Config() *AppConfig {
	e.configMu.RLock()
	defer e.configMu.RUnlock()
	return e.config
}

func (e *Engine) UpdateConfig(cfg *AppConfig) {
	e.configMu.Lock()
	defer e.configMu.Unlock()
	e.config = cfg
}

// --- Event parsing (from xiaoai.ts onEvent) ---

type instructionEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type newLineWrapper struct {
	NewLine string `json:"NewLine"`
	// PreFrozen 客户端在发这条识别结果前已本地冻结原生助手（抢 TTS 请求窗口）。
	PreFrozen          bool `json:"pre_frozen"`
	InstructionGuarded bool `json:"instruction_guarded"`
}

type logMessage struct {
	Header struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		DialogID  string `json:"dialog_id"`
	} `json:"header"`
	Payload struct {
		IsFinal bool `json:"is_final"`
		Results []struct {
			Text string `json:"text"`
		} `json:"results"`
	} `json:"payload"`
}

// SetLocalASRDisabled 关闭/开启本地 ASR（mic 流与 kws 事件）；不影响原生小爱 ASR。
func (e *Engine) SetLocalASRDisabled(disabled bool) { e.localASRDisabled.Store(disabled) }

// LocalASRDisabled 当前是否关闭了本地 ASR。
func (e *Engine) LocalASRDisabled() bool { return e.localASRDisabled.Load() }

// OnEvent is called by the WebSocket server when an event arrives from a client.
func (e *Engine) OnEvent(event connect.Event) {
	var data []byte
	var dataStr string
	if event.Data != nil {
		data = *event.Data
		dataStr = string(data)
	}
	switch event.Event {
	case "playing":
		e.speaker.UpdateStatus(dataStr)
	case "instruction":
		e.handleInstruction(data)
	case "native_probe":
		// 客户端预冻结后的设备状态采样（每 100ms 一条，只用于定位 TTS/音乐时序）
		var p struct {
			TMs            int64  `json:"t_ms"`
			PlayerRaw      string `json:"player_raw"`
			PlayerPID      int    `json:"player_pid"`
			PlayerStopped  bool   `json:"player_stopped"`
			AivsPID        int    `json:"aivs_pid"`
			AivsStopped    bool   `json:"aivs_stopped"`
			MibrainPID     int    `json:"mibrain_pid"`
			MibrainStopped bool   `json:"mibrain_stopped"`
			FrozenPID      int    `json:"frozen_pid"`
		}
		if err := json.Unmarshal(data, &p); err == nil {
			log.Printf("🔬 [native-probe] +%dms player=%s(pid=%d,stopped=%v) aivs=pid%d(stopped=%v) mibrain=pid%d(stopped=%v) frozen=%d",
				p.TMs, strings.TrimSpace(p.PlayerRaw), p.PlayerPID, p.PlayerStopped,
				p.AivsPID, p.AivsStopped, p.MibrainPID, p.MibrainStopped, p.FrozenPID)
		}
	case "native_procs":
		var p struct {
			PS string `json:"ps"`
		}
		if err := json.Unmarshal(data, &p); err == nil {
			log.Printf("🔍 [native-procs]\n%s", p.PS)
		}
	case "kws":
		// 本地 KWS（sherpa）独立读麦克风，与小爱固件互不干扰。
		// 识别到"豆包豆包"这类词时，直接进入对话模式：下一句不必再带触发词。
		var ev struct {
			Type    string `json:"Type"`
			Keyword string `json:"Keyword"`
		}
		_ = json.Unmarshal(data, &ev)
		if e.LocalASRDisabled() {
			log.Printf("🔇 [local-asr] 已关闭，忽略 kws: %s %q", ev.Type, ev.Keyword)
			return
		}
		log.Printf("🔥 唤醒词: %s %q", ev.Type, ev.Keyword)

		if ev.Type == "Keyword" && e.Config().IsAIWakeWord(ev.Keyword) {
			e.armDialog()
			// 若我们正在播报，唤醒词应当打断它
			e.InterruptOnly()
			log.Printf("👂 唤醒词「%s」→ 进入对话模式", ev.Keyword)
			return
		}
		if e.Config().Interrupt.KwsInterrupt {
			e.InterruptOnly()
		}
	}
}

// stripWakeWords 去掉句首的触发词/唤醒词，剩下的才是真正的问题。
func stripWakeWords(text string, words []string) string {
	out := strings.TrimSpace(text)
	changed := true
	for changed {
		changed = false
		for _, w := range words {
			w = strings.TrimSpace(w)
			if w == "" {
				continue
			}
			if strings.HasPrefix(out, w) {
				out = strings.TrimSpace(strings.TrimPrefix(out, w))
				out = strings.TrimLeft(out, "，。！？、,.!? ")
				changed = true
			}
		}
	}
	return out
}

// stripWakePrefix 尽量把句首的唤醒词剥掉，包括被 ASR 听错的模糊形式。
// 「豆邦德邦。」→ 剥掉「豆邦」→ 剩下「德邦。」（交给调用方判断是不是残渣）。
func stripWakePrefix(text string, cfg *AppConfig, extra []string) string {
	out := stripPunct(text)
	trimPunct := func(s string) string {
		return strings.TrimLeft(s, "，。！？、,.!?~～…— \t\r\n")
	}

	// 1) 字面唤醒词（反复剥，处理"豆包豆包"）
	for changed := true; changed; {
		changed = false
		all := append([]string{}, cfg.CallAIKeywords...)
		all = append(all, defaultWakeWords...)
		all = append(all, extra...)
		for _, w := range all {
			w = strings.TrimSpace(w)
			if w != "" && strings.HasPrefix(out, w) {
				out = trimPunct(out[len(w):])
				changed = true
			}
		}
	}

	// 2) 模糊正则命中的句首部分
	for _, re := range wakeFuzzyPatterns {
		if loc := re.FindStringIndex(out); loc != nil && loc[0] == 0 {
			out = trimPunct(out[loc[1]:])
		}
	}
	return out
}

// timeContextWords 命中即认为这句话涉及时间/日期，需要把当前时间告诉豆包。
var timeContextWords = []string{
	"几点", "什么时候", "什么时间", "现在", "今天", "明天", "后天", "昨天", "前天",
	"日期", "几号", "星期", "周几", "礼拜", "多久", "多少分钟", "多少小时",
	"今年", "这个月", "下个月", "上个月", "几月", "倒计时", "闹钟",
}

func needsTimeContext(text string) bool {
	for _, w := range timeContextWords {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

func weekdayCN(t time.Time) string {
	return []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}[int(t.Weekday())]
}

func weekdayShortCN(t time.Time) string {
	return []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[int(t.Weekday())]
}

// XiaoaiSpeaking 判断小爱是否正在说话。
// 事件可能丢（FinishSpeakStream 偶尔没有），所以带一个 30 秒的兜底过期。
func (e *Engine) XiaoaiSpeaking() bool {
	if e.xiaoiSpeakUntil.Load() > 0 && time.Now().UnixMilli() > e.xiaoiSpeakUntil.Load() {
		e.xiaoiSpeaking.Store(false)
		e.xiaoiSpeakUntil.Store(0)
	}
	return e.xiaoiSpeaking.Load()
}

func (e *Engine) setXiaoaiSpeaking(on bool) {
	e.xiaoiSpeaking.Store(on)
	if on {
		e.xiaoiSpeakUntil.Store(time.Now().Add(30 * time.Second).UnixMilli())
	} else {
		e.xiaoiSpeakUntil.Store(0)
	}
}

// UtteranceASR 同一段音频的其它识别途径（都可能为 nil）。
type UtteranceASR struct {
	// Alt 备用模型（Paraformer）：唤醒词没听准时再认一次
	Alt func() string
	// Dialog 对话识别：唤醒之后的指令/问题换成用户选定的引擎再认一次
	Dialog func(current string) string
}

// OnPCMUtterance 处理电脑端 ASR 出来的文本（服务端唤醒模式）。
// 命中唤醒词 → 进入对话模式；已在对话模式 → 直接当问题处理。
func (e *Engine) OnPCMUtterance(text string) { e.onUtterance(text, UtteranceASR{}) }

// OnPCMUtteranceWithASR 同上，额外带上备用模型和对话识别引擎。
func (e *Engine) OnPCMUtteranceWithASR(text string, asr UtteranceASR) { e.onUtterance(text, asr) }

func (e *Engine) onUtterance(text string, asr UtteranceASR) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	cfg := e.Config()

	// 原生唤醒接管进行中：这段话已经由小爱 ASR 交给主助手，本地这路不要再问一遍
	if e.speaker.TakeoverActive() {
		log.Printf("🔇 [PC ASR] 接管中，忽略本地上行识别: %q", text)
		return
	}

	matched := e.matchWake(text, cfg)
	wakeRest := ""
	if matched {
		// 这句要去对话或路由了，灯在这一刻点亮（比"开始思考"早，覆盖识别后的处理阶段）
		e.speaker.ShowThinkingLED()
		wakeRest = strings.TrimSpace(stripWakePrefix(text, cfg, e.customWakeWords()))
	}

	// 噪声门槛：去掉标点后不足 2 个字（"。" "嗯"），一律不当成用户输入。
	// 环境沙沙声经常被 ASR 转成这种碎片，送进豆包只会污染对话。
	if !matched && len([]rune(stripPunct(text))) < 2 {
		log.Printf("🔕 [PC ASR] 文本过短，丢弃: %q", text)
		return
	}

	// 音乐态：判定树在 decideMusic（纯函数，spec voice-routing）。
	d := decideMusic(text, musicInput{
		Matched:  matched,
		WakeRest: wakeRest,
		Playing:  e.speaker.MusicPlaying(),
	})

	// 对话窗口优先于音乐路由：刚被唤醒（或上一轮问答后）说的话就是对话内容，
	// 不能因为"正在放歌 + 没有动作词"被当成歌词碎片丢掉。
	// 白名单快路径仍然优先，保证「下一首」「停一下」这类指令立即执行。
	if e.consumeArmed() {
		switch d.Path {
		case musicPathWhitelist:
			e.execMusicCommand(d.Action, d.Text)
			via := ""
			if matched {
				via = "wake"
			}
			recordRoute(routeEntry{Text: d.Text, Stage: "whitelist", Via: via, Action: d.Action, Result: "dispatched"})
			return
		case musicPathBareWake:
			e.handleMatchedWake("")
			return
		}
		e.speaker.ShowThinkingLED()
		e.handleDialogTurn(text, matched, asr)
		return
	}

	switch d.Path {
	case musicPathBareWake:
		e.handleMatchedWake("") // 只喊了唤醒词，走原来的"我在"
		return
	case musicPathWhitelist:
		e.execMusicCommand(d.Action, d.Text)
		via := ""
		if matched {
			via = "wake"
		}
		recordRoute(routeEntry{Text: d.Text, Stage: "whitelist", Via: via, Action: d.Action, Result: "dispatched"})
		return
	case musicPathRoute:
		if matched {
			log.Printf("🎧 唤醒词 + 指令，先交给路由器: %q", d.Text)
		} else {
			log.Printf("🎵 没唤醒词但带动作词，交给路由器: %q", d.Text)
		}
		if e.routeMusicText(d.Text, d.Via) {
			return
		}
		if d.Via == "wake" {
			e.handleDialogTurn(d.Text, false, asr) // 路由不可用时退回对话
			return
		}
		recordNoiseDrop()
		return
	case musicPathDrop:
		// 歌词碎片只计数，不进列表——否则卡片会被"没唤醒词"刷屏
		recordNoiseDrop()
		return
	}

	rest := ""
	if matched {
		rest = stripWakePrefix(text, cfg, e.customWakeWords())
		if rest != "" && asr.Dialog != nil {
			// 「唤醒词 + 指令」一句话说完：指令部分也按对话引擎再认一遍
			if refined := e.refineDialog(rest, asr.Dialog); refined != rest {
				rest = stripWakePrefix(refined, cfg, e.customWakeWords())
			}
		}
	}
	recordUtterance(utteranceRecord{
		Text:    text,
		Matched: matched,
		Ack:     matched && len([]rune(rest)) <= 3,
	})

	if matched {
		e.handleMatchedWake(rest)
		return
	}

	// 唤醒词可能被听错：用备用模型（Paraformer）再识别一次。
	// 只在"没命中、也不在对话窗口"时执行，正常对话不付这份开销。
	if asr.Alt != nil {
		if altText := strings.TrimSpace(asr.Alt()); altText != "" && altText != text {
			if e.matchWake(altText, cfg) {
				rest := stripWakePrefix(altText, cfg, e.customWakeWords())
				log.Printf("🟡 [PC ASR] 二次识别命中唤醒词: %q（原识别 %q）", altText, text)
				recordUtterance(utteranceRecord{
					Text: altText, Matched: true, Ack: len([]rune(rest)) <= 3,
				})
				e.handleMatchedWake(rest)
				return
			}
		}
	}

	log.Printf("⏭️ [PC ASR] 未命中唤醒词也未在对话模式，忽略: %q", text)
}

// handleDialogTurn 处理对话窗口内的句子：唤醒判定已关闭，整句交给 AI。
// 例外：又只喊了一声唤醒词（"豆包豆包"），按重新唤醒应一声并续窗。
func (e *Engine) handleDialogTurn(text string, matched bool, asr UtteranceASR) {
	if matched {
		if rest := stripWakePrefix(text, e.Config(), e.customWakeWords()); rest == "" {
			recordUtterance(utteranceRecord{Text: text, Matched: true, Ack: true})
			e.handleMatchedWake("")
			return
		}
	}
	if asr.Dialog != nil {
		text = e.refineDialog(text, asr.Dialog)
	}
	// 同一句话可能刚被小爱 ASR 接管处理过
	if !e.claimTurn(text) {
		log.Printf("🔁 [PC ASR] 与原生 ASR 重复，跳过: %q", text)
		return
	}
	recordUtterance(utteranceRecord{Text: text, Dialog: true})
	log.Printf("💬 [PC ASR] 对话模式内: %q", text)
	e.OnMessageAuthorized(text)
}

// handleMatchedWake 命中唤醒词后的统一处理：进对话模式，只喊了唤醒词就应一声。
func (e *Engine) handleMatchedWake(rest string) {
	e.nativeChain.Store(0)
	// 剩余部分本身又命中了唤醒词（"德邦德邦"这种整句被听错的），
	// 说明用户只是喊了一声，没带指令 —— 按裸唤醒处理，别把这段垃圾发给 AI。
	if strings.TrimSpace(rest) != "" && e.matchWake(rest, e.Config()) {
		log.Printf("👂 剩余部分仍是唤醒词 %q，按裸唤醒处理", rest)
		rest = ""
	}
	e.armDialog()
	log.Printf("👂 [PC ASR] 唤醒词命中，进入对话模式；剩余=%q", rest)
	if len([]rune(rest)) <= 3 {
		// 只喊了唤醒词（或者剩下的只是"德邦"这种听错的残渣）：应一声
		go func() {
			if err := e.speaker.PlayTTS("我在", true); err != nil {
				log.Printf("⚠️ 唤醒应答播放失败: %v", err)
			}
			w := e.continueWindow()
			e.armDialogFor(w)
			log.Printf("👂 继续对话（%s 内直接说）", w)
		}()
		return
	}
	e.OnMessageAuthorized(rest)
}

// refineDialog 用对话识别引擎重认一句；失败或结果为空时保留原文。
func (e *Engine) refineDialog(current string, dialog func(string) string) string {
	if dialog == nil {
		return current
	}
	refined := strings.TrimSpace(dialog(current))
	if refined == "" {
		return current
	}
	if refined != current {
		log.Printf("🎯 [对话识别] %q（%d字） → %q（%d字）",
			current, len([]rune(current)), refined, len([]rune(refined)))
	}
	return refined
}

// armDialog 进入对话模式，窗口内置位。
func (e *Engine) armDialog() {
	e.armDialogFor(dialogArmWindow)
}

// continueWindow 返回当前配置的对话窗口时长。
func (e *Engine) continueWindow() time.Duration {
	sec := defaultContinueSeconds
	if cfg := e.Config(); cfg != nil && cfg.ContinuationSeconds > 0 {
		sec = cfg.ContinuationSeconds
	}
	return time.Duration(sec) * time.Second
}

func (e *Engine) armDialogFor(d time.Duration) {
	e.armedUntil.Store(time.Now().Add(d).UnixMilli())
}

// disarmDialog 关闭对话模式（用户说拜拜/切到小爱指令时）。
func (e *Engine) disarmDialog() {
	e.armedUntil.Store(0)
}

// EndConversation 结束当前对话：立即关闭续窗，并且本轮回复结束后不再自动续窗。
// 给 agent 的 MCP 工具用（用户说"退下吧""先这样"之类时由模型调用）。
func (e *Engine) EndConversation() {
	e.endAfterReply.Store(true)
	e.disarmDialog()
	log.Printf("👋 结束对话，回到唤醒监听")
}

// isExitPhrase 判断是否是结束对话的话。
func isExitPhrase(text string) bool {
	for _, w := range exitWords {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// consumeArmed 若处于对话模式窗口内，消费掉并返回 true。
func (e *Engine) consumeArmed() bool {
	until := e.armedUntil.Load()
	if until == 0 {
		return false
	}
	e.armedUntil.Store(0)
	now := time.Now().UnixMilli()
	if now > until {
		// 窗口内一直没人说话：进入睡眠，回到只认唤醒词的状态
		log.Printf("💤 对话窗口已超时（%.0f 秒没说话），回到唤醒监听", float64(now-until)/1000)
		return false
	}
	return true
}

func (e *Engine) handleInstruction(data []byte) {
	line, preFrozen := parseInstructionEvent(data)
	var guardEvent newLineWrapper
	_ = json.Unmarshal(data, &guardEvent)
	if line == "" {
		return
	}

	var msg logMessage
	if err := json.NewDecoder(strings.NewReader(line)).Decode(&msg); err != nil {
		return
	}
	log.Printf("🧾 [instruction] %s.%s final=%v pre_frozen=%v", msg.Header.Namespace, msg.Header.Name, msg.Payload.IsFinal, preFrozen)

	// 客户端已经先冻结了 aivs。本轮如果走不到接管（退出词/家电指令/未启用等），
	// 必须立刻放行，否则小爱原生回答会被我们自己冻住。
	takeoverHandled := false
	defer func() {
		if preFrozen && !takeoverHandled {
			e.speaker.ReleaseNativePreFreeze("本句未进入接管")
		}
	}()

	// 钩子已认领的下行动作即使出现在固件原始日志中，也不能当成实际播报。
	if guardEvent.InstructionGuarded && msg.Header.Namespace != "SpeechRecognizer" &&
		!(msg.Header.Namespace == "Dialog" && msg.Header.Name == "Finish") {
		log.Printf("🛡️ [native-guard] 忽略已拦截指令日志：%s.%s", msg.Header.Namespace, msg.Header.Name)
		return
	}
	// 小爱的播报事件：用来判断"现在是不是小爱在说话"，
	// 避免它的声音被我们的麦克风录下来又送给豆包，也避免两边同时出声。
	switch {
	case msg.Header.Namespace == "SpeechSynthesizer" && msg.Header.Name == "Speak":
		if e.speaker.TakeoverActive() || e.speaker.LateNativeTTSSuppressed() {
			// 冻结晚于 TTS 请求，或刚解冻 aivs 补发了排队的回答：
			// 再清一次 mibrain 队列兜底（不影响音乐）
			log.Printf("⚠️ [native-tts] 接管/兜底窗口内收到原生 Speak 事件，清空 mibrain 队列")
			go e.speaker.StopNativeTTS("Speak 事件兜底")
			return
		}
		e.setXiaoaiSpeaking(true)
		log.Printf("🔊 [native-tts] 小爱开始播报（暂停上行识别）")
		return
	case msg.Header.Namespace == "SpeechSynthesizer" && msg.Header.Name == "FinishSpeakStream":
		e.setXiaoaiSpeaking(false)
		log.Printf("🔇 [native-tts] 小爱播报结束")
		return
	case msg.Header.Namespace == "Dialog" && msg.Header.Name == "Finish":
		e.setXiaoaiSpeaking(false)
		if !e.speaker.TakeoverActive() {
			e.speaker.EndNativeASRDialog(msg.Header.DialogID)
		}
		return
	}

	if msg.Header.Namespace != "SpeechRecognizer" || msg.Header.Name != "RecognizeResult" {
		return
	}
	if !msg.Payload.IsFinal {
		e.speaker.ObserveNativeASRDialog(msg.Header.DialogID)
		return
	}
	if len(msg.Payload.Results) == 0 || strings.TrimSpace(msg.Payload.Results[0].Text) == "" {
		if !e.speaker.TakeoverActive() {
			e.speaker.EndNativeASRDialog(msg.Header.DialogID)
		}
		return
	}

	text := msg.Payload.Results[0].Text
	log.Printf("🗣️ 用户: %s（pre_frozen=%v）", text, preFrozen)
	// 最近识别面板也记一份，标记来源；否则只能看到 PC ASR，分不清这句是谁识别的。
	recordUtterance(utteranceRecord{Text: text, Source: "xiaoai"})
	cfg := e.Config()

	// 退出词：结束对话，不再续窗
	if isExitPhrase(text) {
		e.disarmDialog()
		log.Printf("👋 结束对话: %q", text)
		return
	}

	// 原生唤醒接管：小爱的 ASR final 直接交给主助手
	if e.tryNativeTakeover(text, guardEvent.InstructionGuarded) {
		takeoverHandled = true
		return
	}

	// 对话模式：唤醒词/上一句之后，这一段话直接进 AI
	if e.consumeArmed() {
		// 家电控制这类指令仍然交还小爱，别被 AI 抢走
		if kw := cfg.nativeKeyword(text); kw != "" {
			log.Printf("🏠 对话模式中命中原生指令（%s），交还小爱", kw)
			e.disarmDialog()
			return
		}
		if !e.claimTurn(text) {
			log.Printf("🔁 与本地 ASR 重复（对话模式），跳过: %q", text)
			return
		}
		log.Printf("👂 对话模式：本句直接进 AI")
		e.OnMessage(text)
		return
	}

	// 只喊触发词、没有实际内容 → 进入对话模式，等下一句
	if cfg.IsBareTrigger(text) {
		e.armDialog()
		// 只清小爱 TTS 的 mibrain 队列；不要用 StopNativePlayback，
		// 那会把 mediaplayer 整条队列（包括音乐）一起停掉。
		go e.speaker.StopNativeTTS("裸触发词")
		log.Printf("👂 已进入对话模式（%s 内下一句直接进 AI）", dialogArmWindow)
		return
	}

	decision, keyword := cfg.instructionDecision(text)
	switch decision {
	case instructionDecisionCallAI:
		if !e.claimTurn(text) {
			log.Printf("🔁 与本地 ASR 重复（call_ai），跳过: %q", text)
			return
		}
		log.Printf("✅ 语音进入 AI: call_ai keyword=%q", keyword)
		e.OnMessage(text)
	case instructionDecisionInterruptOnly:
		// 服务端唤醒开启时，小爱的 ASR 和我们的麦克风会同时听到同一句话。
		// 这条 instruction 往往只是"回声"，在这里打断会把我们正在进行的 AI 请求掐掉。
		if cfg.ASR.Enabled {
			log.Printf("🔇 服务端唤醒模式下忽略小爱侧打断: %q", text)
			return
		}
		log.Printf("⏹️ 只打断不调用 AI: interrupt keyword=%q", keyword)
		e.InterruptOnly()
	default:
		log.Printf("⏭️ 不处理语音: 未匹配 call_ai_keywords=%v 或 interrupt.keywords=%v match=%s text=%q", cfg.CallAIKeywords, cfg.Interrupt.Keywords, cfg.Interrupt.MatchMode, text)
		return
	}
}

// turnDedupeWindow 同一句话被两路 ASR 听到时，这个窗口内只处理一次。
const turnDedupeWindow = 2500 * time.Millisecond

// maxNativeChainTurns 一次「小爱同学」唤醒后，最多自动续开几次原生收音。
const maxNativeChainTurns = 6

// turnKey 归一化一句话：去标点、去唤醒词，用于判断两路 ASR 是不是在说同一句。
func turnKey(text string) string {
	s := strings.Map(func(r rune) rune {
		if r >= '！' && r <= '～' {
			r -= 0xfee0
		}
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
	s = stripPunct(s)
	for _, w := range []string{"小爱同学", "豆包豆豆", "豆包"} {
		s = strings.ReplaceAll(s, w, "")
	}
	return strings.TrimSpace(s)
}

// claimTurn 登记这一句；窗口内出现过同样的句子就返回 false。
func (e *Engine) claimTurn(text string) bool {
	key := turnKey(text)
	if key == "" {
		return true
	}
	now := time.Now()
	e.turnMu.Lock()
	defer e.turnMu.Unlock()
	if e.turnText == key && now.Sub(e.turnAt) < turnDedupeWindow {
		return false
	}
	e.turnText = key
	e.turnAt = now
	return true
}

// releaseTurn 撤销登记（接管没成功时，让本地 ASR 还能处理这一句）。
func (e *Engine) releaseTurn() {
	e.turnMu.Lock()
	e.turnAt = time.Time{}
	e.turnMu.Unlock()
}

// tryNativeTakeover 处理“小爱同学”这一轮：冻结原生助手，把文本交给主助手。
// 不限于音乐态——不放歌时同样接管，只是没有音乐需要恢复。
// 返回 true 表示已经接管，调用方不要再走下面的路由分支。
func (e *Engine) tryNativeTakeover(text string, guarded ...bool) bool {
	cfg := e.Config()
	if !cfg.NativeWake.Enabled || strings.TrimSpace(text) == "" {
		return false
	}
	// 家电类指令仍交还小爱：冻了原生助手但动作已经执行会很割裂
	if kw := cfg.nativeKeyword(text); kw != "" {
		log.Printf("🏠 接管跳过：命中原生指令（%s），交还小爱", kw)
		return false
	}
	// 同一句话可能已经被本地 ASR 送去豆包了：不重复问，但还是要冻结小爱，
	// 否则它会自己答一遍。用较短的上限兜底，避免白冻 25 秒。
	if !e.claimTurn(text) {
		if err := e.speaker.BeginNativeTakeover(8*time.Second, guarded...); err != nil {
			log.Printf("⚠️ 重复句补冻结失败: %v", err)
		} else {
			log.Printf("🔁 接管跳过：本地 ASR 已处理过 %q，只冻结小爱", text)
		}
		return true
	}
	// 冻结前先记位置：原生 dialog 一旦把播放器停掉，位置就取不到了。
	e.speaker.RememberMusicPosition()
	max := time.Duration(cfg.NativeWake.MaxSeconds) * time.Second
	start := time.Now()
	if err := e.speaker.BeginNativeTakeover(max, guarded...); err != nil {
		e.releaseTurn()
		log.Printf("⚠️ 原生唤醒接管失败，回退原生路径: %v", err)
		return false
	}
	log.Printf("⏱️ [native] 接管冻结完成，耗时 %v（music=%v）", time.Since(start).Round(time.Millisecond), e.speaker.MusicPlaying())
	e.setXiaoaiSpeaking(false)
	e.nativeTurn.Store(true)
	log.Printf("❄️ 原生唤醒接管：%q 交给主助手", text)
	// 已经由原生唤醒触发，跳过唤醒词二次校验
	e.OnMessageAuthorized(text)
	return true
}

func parseInstructionEvent(data []byte) (string, bool) {
	var wrapper newLineWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return "", false
	}
	return wrapper.NewLine, wrapper.PreFrozen
}

func instructionLineFromEventData(data []byte) string {
	line, _ := parseInstructionEvent(data)
	return line
}

// --- Message handling (from MiGPTEngine.onMessage) ---

// InterruptOnly 仅打断当前 AI 回复，不处理新消息（用于 kws 事件）
func (e *Engine) InterruptOnly() {
	e.mu.Lock()
	if e.cancelFunc != nil {
		e.cancelFunc()
		e.cancelFunc = nil
	}
	e.mu.Unlock()
	e.speaker.StopTTS()
	log.Println("⏹️ 唤醒词打断")
}

// OnMessage 处理来自小爱 instruction 的文本（需要再校验一次触发词）。
func (e *Engine) OnMessage(text string) { e.startMessage(text, false) }

// OnMessageAuthorized 处理已经过触发词判定（电脑端 ASR 唤醒）的文本，跳过二次校验。
func (e *Engine) OnMessageAuthorized(text string) { e.startMessage(text, true) }

func (e *Engine) startMessage(text string, authorized bool) {
	// Cancel any previous AI call / TTS in progress.
	e.mu.Lock()
	if e.cancelFunc != nil {
		e.cancelFunc()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancelFunc = cancel
	e.lastMsgTS = time.Now().UnixMilli()
	e.mu.Unlock()
	turnID := e.turnSeq.Add(1)

	stopTTS := func() {
		// 停掉小爱自己的回复：noop 采集模式下固件的 ASR 和原生回复是同一套流程，
		// 我们只是旁路读识别结果，不主动停就会"小爱 + 豆包"两套声音叠着说。
		//
		// 但音乐不能停：音乐也走 mediaplayer，一句无关的闲聊就把它掐掉太伤了。
		// 音乐态下交给 miplayer 自己抢占设备，播报结束后由 Speaker 负责接回去。
		if e.speaker.MusicPlaying() {
			// 播报会抢音频设备，位置会丢；先记下来，播完跳回去
			e.speaker.RememberMusicPosition()
			log.Println("🎵 音乐播放中，跳过停原生播放")
		} else {
			// 不再用 StopNativePlayback 全量 stop：MusicPlaying() 是内存状态，
			// 重启/原生停播后可能与真实播放器不一致，全量 stop 会把 common 上的音乐清掉。
			// 只清小爱 TTS 的 mibrain 队列，音乐队列一律不动。
			e.speaker.StopNativeTTS("开始处理新消息")
		}
		if err := e.speaker.StopTTS(); err != nil {
			log.Printf("⚠️ stop_tts timeout/failed: %v", err)
		}
	}
	if e.Config().shouldStopTTSBeforeHandling(text) {
		stopTTS()
	} else {
		go stopTTS() // 轻打断：终止 client 端当前 TTS，不阻塞 AI 请求准备
	}
	takeoverEpoch := e.speaker.TakeoverEpoch()
	go func() {
		e.handleMessage(ctx, text, authorized)
		// 已经被更新的轮次接管：收尾（解冻/续听）交给新轮次，老的不能插手。
		if e.turnSeq.Load() != turnID {
			log.Printf("⏭️ 旧轮次收尾跳过（turn %d 已被新轮次接管）", turnID)
			return
		}
		// 接管必须等整轮回复播完才结束：我们的 TTS 是逐句播放的，
		// 单句结束就解冻会让小爱在后面几句播放时把原生 TTS 说出来。
		if takeoverEpoch != 0 {
			e.speaker.EndNativeTakeoverFor(takeoverEpoch, "回答结束")
		}
		// 这一轮是小爱唤醒触发的：不续开本地对话窗口。
		// 本地 ASR 听到的往往是小爱的播报或环境声，继续送给豆包只会自说自话。
		if e.nativeTurn.Swap(false) {
			e.disarmDialog()
			e.continueNativeAfterReply(ctx, turnID)
			return
		}
		// 模型主动结束对话（MCP end_conversation）：本轮说完就回到唤醒监听，不再续窗
		if e.endAfterReply.Swap(false) {
			e.disarmDialog()
			log.Printf("👋 本轮结束后不再续窗，回到唤醒监听")
			return
		}
		// 正常说完（没被打断）就自动续开对话窗口，用户可以直接接着说
		if ctx.Err() == nil {
			w := e.continueWindow()
			e.armDialogFor(w)
			log.Printf("👂 继续对话（%s 内直接说，不用再喊）", w)
		}
	}()
}

// continueNativeAfterReply 只在非音乐态自动续听。
// 原生续听会再次打开麦克风并可能暂停播放器，不能在刚恢复音乐后立即重开。
func (e *Engine) continueNativeAfterReply(ctx context.Context, turnID uint64) {
	if ctx.Err() != nil || e.turnSeq.Load() != turnID {
		return
	}
	if e.speaker.MusicPlaying() {
		e.nativeChain.Store(0)
		log.Printf("🎵 音乐态不自动重开原生麦克风；下次说话请再喊小爱同学")
		return
	}
	if n := e.nativeChain.Add(1); n < maxNativeChainTurns {
		time.Sleep(400 * time.Millisecond)
		if ctx.Err() != nil || e.turnSeq.Load() != turnID {
			return
		}
		if e.speaker.MusicPlaying() {
			e.nativeChain.Store(0)
			log.Printf("🎵 音乐已开始播放，取消自动原生续听")
			return
		}
		seq := e.speaker.BeginNativeASRWindow()
		if err := e.speaker.ContinueNativeDialog(); err == nil {
			log.Printf("🎤 原生续听已开启（连续第 %d 轮，最多 %d 轮）", n+1, maxNativeChainTurns)
			// 原生 VAD 通常约 6 秒结束；空 final/Finish 正常关窗，超时负责兜底。
			time.AfterFunc(8*time.Second, func() { e.speaker.EndNativeASRWindowFor(seq) })
			return
		} else {
			e.speaker.EndNativeASRWindowFor(seq)
			log.Printf("⚠️ 原生续听失败: %v", err)
		}
	}
	e.nativeChain.Store(0)
	log.Printf("🔇 原生唤醒轮次结束：不续开本地对话窗口（下次说话请再喊小爱同学）")
}

func (e *Engine) handleMessage(ctx context.Context, text string, authorized bool) {
	cfg := e.Config()
	// 1. Check custom replies
	for _, r := range cfg.CustomReplies {
		if r.Match == text {
			if r.URL != "" {
				log.Printf("🔊 自定义回复 (URL): %s", r.URL)
				e.speaker.PlayURL(r.URL, false)
			} else if r.Text != "" {
				log.Printf("🔊 自定义回复: %s", r.Text)
				e.speaker.PlayTTS(r.Text, false)
			}
			return
		}
	}

	// 2. Check keyword match（电脑端 ASR 唤醒的句子已经判定过，跳过）
	callAIKeyword := cfg.callAIKeyword(text)
	if callAIKeyword == "" {
		if !authorized {
			log.Printf("⏹️ 不调用 AI: 未匹配 call_ai_keywords=%v text=%q", cfg.CallAIKeywords, text)
			return
		}
		callAIKeyword = "pc-asr"
	}
	log.Printf("🤖 准备调用 AI: keyword=%q model=%s text=%q", callAIKeyword, cfg.GetLLM().Model, text)

	// 3. Stream from OpenAI → sentence-by-sentence TTS
	e.addHistory("user", text)

	// 思考态：点亮播放灯，第一句 TTS 出声前熄灭
	e.speaker.ShowThinkingLED()
	defer e.speaker.HideThinkingLED()

	reply, err := e.streamAndPlay(ctx, text)
	// codex 超时/挂掉：一个字都还没说，就换直连模型再试一次，
	// 总比直接播"出错了"强（实测 DeepSeek 偶发十几秒不吐字）。
	if err != nil && ctx.Err() == nil && strings.TrimSpace(reply) == "" && cfg.Chat.UseCodex() && e.client != nil {
		log.Printf("⚠️ codex 失败（%v），降级到直连模型重试", err)
		reply, err = e.streamAndPlayOn(ctx, text, true)
	}
	if err != nil {
		if ctx.Err() != nil {
			log.Println("⏹️ AI 回复被中断")
			return
		}
		log.Printf("❌ AI error: %v", err)
		msg := cfg.ErrorMessage
		if msg == "" {
			msg = "出错了，请稍后再试吧！"
		}
		e.speaker.PlayTTS(msg, true)
		return
	}

	if reply != "" {
		e.addHistory("assistant", reply)
	}
}

func (e *Engine) matchesKeyword(text string) bool {
	return e.Config().shouldCallAI(text)
}

// aiStream 抽象 OpenAI 流与豆包网页流，两者 Recv/Close 签名一致。
type aiStream interface {
	Recv() (openai.ChatCompletionStreamResponse, error)
	Close() error
}

// SetCodexAppServer 注入常驻的 codex app-server。
func (e *Engine) SetCodexAppServer(s *CodexAppServer) {
	e.codexServer = s
	if s != nil {
		e.codexRouter = newCodexRouter(s, func() string {
			if cfg := e.Config(); cfg != nil {
				return cfg.MusicRouter.Prompt
			}
			return ""
		})
	}
}

// SetMusicControls 注入音乐控制：下一首/停止。
func (e *Engine) SetMusicControls(next func(context.Context) (string, error), stop func()) {
	e.musicNext = next
	e.musicStop = stop
}

// SetToolAccess 注入"路由可以直接调用的工具集"（复用 MCP 的工具定义与执行器）。
func (e *Engine) SetToolAccess(defs func() []openai.Tool, run func(ctx context.Context, name string, args map[string]any) (string, error)) {
	e.toolDefs = defs
	e.toolRun = run
}

// SetMusicRouting 注入音乐态语义路由的依赖：
// desc 提供当前歌曲/音量（给路由 prompt 当上下文），playQuery 按描述搜歌并播放
// （shuffle=true 表示用户要“随机放点”）。
func (e *Engine) SetMusicRouting(
	desc func() (song, artists string, volume int),
	playQuery func(ctx context.Context, query string, shuffle bool) (string, error),
) {
	e.musicDesc = desc
	e.musicPlayQuery = playQuery
}

// SetCodexClient 注入 Codex CLI 客户端。
func (e *Engine) SetCodexClient(c *CodexClient) { e.codex = c }

// SetWakeStore 注入用户自定义唤醒词表。
func (e *Engine) SetWakeStore(s *wakeStore) { e.wakeStore = s }

// customWakeWords 返回用户自定义的词（可能为空）。
func (e *Engine) customWakeWords() []string {
	if e.wakeStore == nil {
		return nil
	}
	return e.wakeStore.List()
}

// matchWake 综合内置词表和用户自定义词判断是否唤醒。
func (e *Engine) matchWake(text string, cfg *AppConfig) bool {
	// 「小爱同学」保留给音箱原生唤醒：本地一律不认这个词。
	// 这样两套唤醒是"二选一"——喊「小爱同学」走原生 + 豆包接管，
	// 喊「小爱」（或豆包豆豆）才唤醒本地链路，不会因为包含关系两边同时进。
	if strings.Contains(stripPunct(text), "小爱同学") {
		return false
	}
	if cfg.IsAIWakeWord(text) {
		return true
	}
	if e.wakeStore != nil && e.wakeStore.Contains(text) {
		return true
	}
	return false
}

// SetDoubaoWeb 注入豆包网页直连客户端（配置启用时由 runtime 调用）。
func (e *Engine) SetDoubaoWeb(w *DoubaoWeb) {
	e.doubaoWeb = w
}

// useDoubaoWeb 判断本次请求是否应该走豆包网页直连。
func (e *Engine) useDoubaoWeb() *DoubaoWeb {
	if e.doubaoWeb == nil {
		return nil
	}
	cfg := e.Config()
	if cfg == nil || !cfg.DoubaoWeb.Enabled {
		return nil
	}
	if !cfg.Chat.UseDoubaoWeb() {
		return nil // 配置指定了别的后端
	}
	if !e.doubaoWeb.LoggedIn() {
		log.Printf("⚠️ doubao_web 已启用但未登录，回退到 llm.base_url")
		return nil
	}
	return e.doubaoWeb
}

// hasSpeakableContent 判断文本里有没有值得念出来的字符（中日韩文字/字母/数字）。
// 纯标点或空白（如 "\n\n"、"”"、"。"）不送 TTS，否则会念出无意义的停顿甚至报错。
func hasSpeakableContent(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// --- OpenAI streaming + sentence-by-sentence TTS ---

func (e *Engine) streamAndPlay(ctx context.Context, userText string) (reply string, err error) {
	return e.streamAndPlayOn(ctx, userText, false)
}

// streamAndPlayOn 同 streamAndPlay；forcePlain=true 时跳过 codex，直接走直连模型
// （codex 挂掉/超时时的降级路径）。
func (e *Engine) streamAndPlayOn(ctx context.Context, userText string, forcePlain bool) (reply string, err error) {
	messages := e.buildMessages(userText)
	log.Printf("🤖 调用 AI 中: messages=%d", len(messages))

	cfg := e.Config()

	var stream aiStream
	if !forcePlain && cfg.Chat.UseCodex() && e.codexServer != nil {
		log.Printf("🧩 走 Codex 常驻（app-server）")
		stream = newCodexAppStream(ctx, e.codexServer, userText)
	} else if !forcePlain && cfg.Chat.UseCodex() && e.codex != nil {
		log.Printf("🧩 走 Codex CLI（profile=%s）", cfg.Codex.Profile)
		stream = newCodexStream(ctx, e.codex, userText)
	} else if web := e.useDoubaoWeb(); web != nil {
		// 豆包服务端按 conversation_id 保存上下文，续聊时只发当前这句即可。
		// 只有开新会话时才把系统设定带上，否则用户消息里会混进一大段提示词。
		// 系统提示词不发：豆包网页端自带人格，而且会话上下文由服务端保存。
		// 发过去只会让聊天记录里混进一大段"你是一个智能助手…"，还把时间重复两遍。
		//
		// 唯一例外是时间：豆包没有时钟，问"今天几号"会瞎猜。
		// 只在问题涉及时间时才附一句当前时间，避免每条消息都被污染。
		// 每条消息尾部附一个极简的时间戳，豆包才有时间概念。
		now := time.Now().In(loadTZ(cfg.Prompt.Timezone))
		prompt := userText + " [" + now.Format("01-02 15:04") + " " + weekdayShortCN(now) + "]"

		// 新会话时把系统提示词带上（豆包网页版没有 system 角色）。
		// 会话会持续复用，所以只在开头付一次成本。
		if !web.HasConversation() {
			if sys := strings.TrimSpace(cfg.Prompt.System); sys != "" {
				prompt = systemPromptWithEmotion(sys) + "\n\n" + prompt
			}
		}
		log.Printf("🍬 走豆包网页直连（新会话=%v）", !web.HasConversation())
		stream = newDoubaoChatStream(ctx, web, prompt)
	} else {
		s, err := e.client.CreateChatCompletionStream(ctx, openai.ChatCompletionRequest{
			Model:    cfg.GetLLM().Model,
			Messages: messages,
			Stream:   true,
		})
		if err != nil {
			return "", fmt.Errorf("create stream: %w", err)
		}
		stream = s
	}
	defer stream.Close()

	// 本轮回复播完之后再开始放歌（MCP 点歌只是排队）；
	// 被打断或出错就取消，别让下一轮莫名其妙响起音乐。
	defer func() {
		if err != nil || ctx.Err() != nil {
			e.speaker.ClearPendingMusic()
			return
		}
		if err := e.speaker.PlayPendingMusic(); err != nil {
			log.Printf("⚠️ 音乐播放失败: %v", err)
		}
	}()

	// 豆包 TTS 支持预合成：每句一产出就并行合成，播放器按顺序放，消除句间空档。
	// 本地 tts_play.sh 无法预生成，退回原来的"合成即播"。
	var pipe *speechPipeline
	if e.speaker.CanPreSynthesize() {
		pipe = newSpeechPipeline(ctx, e.speaker)
		defer pipe.Close()
	}
	speak := func(sentence, emotion string) error {
		if pipe != nil {
			pipe.AddWithEmotion(sentence, emotion)
			return nil
		}
		return e.playTTS(ctx, sentence, emotion)
	}

	var fullReply strings.Builder
	var sentenceBuf strings.Builder
	// prefixPlayed 确保 ReplyPrefix 在整段 AI 回复里只播一次。
	// 必须在 LLM 第一个 token 到达后才播——否则等不到 token 就报 prefix 会让用户听到"AI回复...
	// 然后才是真正的错误提示"，更怪。
	prefixPlayed := false
	// 模型一般只在开头标一次情绪，后面沿用；标记变了就切
	curEmotion := ""

	for {
		// 主动检查 ctx 取消：用户说"闭嘴"等打断词时，InterruptOnly 会 cancelFunc()，
		// 这里在每次 stream.Recv 前先看一眼，能尽快退出循环，不再消费已 buffer 的 token、
		// 也不再合成新句子。如果只依赖 stream.Recv 自己感知 ctx 取消，buffered token 会被处理完,
		// 表现为"我说了闭嘴，AI 还又输出了 1-2 句话"。
		if ctx.Err() != nil {
			return fullReply.String(), ctx.Err()
		}

		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			// Flush remaining text
			if sentenceBuf.Len() > 0 {
				sentence, tag := splitEmotionTag(trimForSpeech(sentenceBuf.String()))
				if tag != "" {
					curEmotion = tag
				}
				emotion := curEmotion
				if sentence == "" {
					break
				}
				if emotion != "" {
					log.Printf("AI[%s]: %s", emotion, sentence)
				} else {
					log.Printf("AI: %s", sentence)
				}
				if hasSpeakableContent(sentence) {
					if playErr := e.playReplyPrefixOnce(ctx, &prefixPlayed); playErr != nil {
						return fullReply.String(), playErr
					}
					if playErr := speak(sentence, emotion); playErr != nil {
						return fullReply.String(), playErr
					}
				}
			}
			if pipe != nil {
				pipe.Close() // 等剩下的句子播完
			}
			break
		}
		if err != nil {
			// ctx 取消导致的 stream 错误：当成正常打断退出，不再补一句 errMsg TTS
			if ctx.Err() != nil {
				return fullReply.String(), ctx.Err()
			}
			return fullReply.String(), fmt.Errorf("stream recv: %w", err)
		}

		if len(resp.Choices) == 0 {
			continue
		}
		token := resp.Choices[0].Delta.Content
		if token == "" {
			continue
		}

		fullReply.WriteString(token)
		sentenceBuf.WriteString(token)

		// 只按标点断句（逗号、字数阈值都不要）：切得太碎语调会很不自然。
		for {
			head, rest, ok := splitAtFirstEnd(sentenceBuf.String())
			if !ok {
				break
			}
			sentenceBuf.Reset()
			sentenceBuf.WriteString(rest)
			if ctx.Err() != nil {
				return fullReply.String(), ctx.Err()
			}
			sentence, tag := splitEmotionTag(trimForSpeech(head))
			if tag != "" {
				curEmotion = tag // 模型通常只标第一句，后面沿用
			}
			emotion := curEmotion
			if sentence == "" {
				break // 整句只是个标记
			}
			// 太短的片段（"1"、"国内"）不单独合成，塞回缓冲等下一句
			if len([]rune(sentence)) < 4 {
				sentenceBuf.Reset()
				sentenceBuf.WriteString(trimForSpeech(head) + rest)
				break
			}
			if emotion != "" {
				log.Printf("AI[%s]: %s", emotion, sentence)
			} else {
				log.Printf("AI: %s", sentence)
			}
			if hasSpeakableContent(sentence) {
				if playErr := e.playReplyPrefixOnce(ctx, &prefixPlayed); playErr != nil {
					return fullReply.String(), playErr
				}
				if playErr := speak(sentence, emotion); playErr != nil {
					return fullReply.String(), playErr
				}
			}
		}
	}

	return fullReply.String(), nil
}

// playReplyPrefixOnce 在整段 AI 回复的第一次 TTS 出口前，播一次 ReplyPrefix。
// 用 *bool 让调用方做"是否第一次"判断，避免每个句子前都重复播。
//
// 复用 e.playTTS：它阻塞直到 tts_play.sh 播完，自然保证前缀先于正文播，且响应 ctx 取消
// （用户在 LLM 思考阶段说"闭嘴"时不再播）。
func (e *Engine) playReplyPrefixOnce(ctx context.Context, played *bool) error {
	if *played {
		return nil
	}
	*played = true
	prefix := strings.TrimSpace(e.Config().ReplyPrefix)
	if prefix == "" {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	log.Printf("🤖 AI 回复前缀: %s", prefix)
	if err := e.playTTS(ctx, prefix, ""); err != nil {
		log.Printf("⚠️ AI 回复前缀 TTS 失败: %v", err)
		return err
	}
	return nil
}

func (e *Engine) playTTS(ctx context.Context, text, emotion string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Wait 2 seconds after aborting XiaoAI for TTS service to recover.
	// This matches the behavior in the original config.ts.
	errCh := make(chan error, 1)
	go func() {
		errCh <- e.speaker.PlayTTSVoice(text, true, emotion)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isSentenceEnd 判断是否是断句符。
func isSentenceEnd(r rune) bool {
	switch r {
	// 刻意不含 '.' '：' ':'：它们会把「1.」「国内：」切成碎片
	case '。', '！', '？', '；', '\n', '!', '?', '~', '～', '…', '—':
		return true
	}
	return false
}

// splitAtFirstEnd 在第一个断句符处切开：head 是含标点的那一句，rest 是剩余。
// 一次 token 里可能塞进好几句话（搜索结果常见），必须逐句切出来，
// 否则整段会被当成一句合成，听起来又长又平。
func splitAtFirstEnd(s string) (head, rest string, ok bool) {
	for i, r := range s {
		if isSentenceEnd(r) {
			return s[:i+len(string(r))], s[i+len(string(r)):], true
		}
	}
	return "", s, false
}

func hasSentenceEnd(s string) bool {
	for _, r := range s {
		switch r {
		case '。', '！', '？', '；', '\n', '.', '!', '?',
			'~', '～', '…', '—', '：', ':':
			return true
		}
	}
	return false
}

// --- Chat history management ---

func (e *Engine) buildMessages(userText string) []openai.ChatCompletionMessage {
	e.historyMu.Lock()
	historyLen := len(e.history)
	e.historyMu.Unlock()

	cap := historyLen + 1
	cfg := e.Config()
	timeLine := ""
	if cfg.Prompt.InjectTime {
		timeLine = currentTimeLine(cfg.Prompt.Timezone, time.Now())
	}
	if cfg.Prompt.System != "" {
		cap++
	}
	// 把"现在到底有没有在放歌"塞进系统提示：模型很爱凭记忆回答"正在放着呢"，
	// 服务端重启后队列恢复了但播放器是空的，它就会骗用户。
	musicLine := "[音乐] 当前没有音乐在播放；用户要听歌请直接调用 play_music，不要说\"正在播放\"。"
	if e.speaker.MusicPlaying() {
		musicLine = "[音乐] 当前有音乐在播放。"
	}
	msgs := make([]openai.ChatCompletionMessage, 0, cap)
	sys := systemPromptWithEmotion(cfg.Prompt.System)
	if timeLine != "" {
		if sys != "" {
			sys += "\n"
		}
		sys += timeLine
	}
	if sys != "" {
		sys += "\n"
	}
	sys += musicLine
	if strings.Contains(sys, "{{now}}") || strings.Contains(sys, "{{date}}") {
		now := time.Now().In(loadTZ(cfg.Prompt.Timezone))
		sys = strings.ReplaceAll(sys, "{{now}}", now.Format("2006年1月2日 15:04"))
		sys = strings.ReplaceAll(sys, "{{date}}", now.Format("2006年1月2日"))
	}
	if sys != "" {
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: sys,
		})
	}
	e.historyMu.Lock()
	msgs = append(msgs, e.history...)
	e.historyMu.Unlock()

	// 同时拼进最后一条 user 消息（仅副本，不写入历史），避免中转丢弃 system。
	if timeLine != "" {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == openai.ChatMessageRoleUser {
				msgs[i].Content = timeLine + "\n" + msgs[i].Content
				break
			}
		}
	}

	return msgs
}

func loadTZ(name string) *time.Location {
	if name == "" {
		name = "Asia/Shanghai"
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}

func currentTimeLine(tz string, now time.Time) string {
	n := now.In(loadTZ(tz))
	wd := [...]string{"日", "一", "二", "三", "四", "五", "六"}[n.Weekday()]
	return fmt.Sprintf("[当前时间：%d年%d月%d日 星期%s %02d:%02d]", n.Year(), int(n.Month()), n.Day(), wd, n.Hour(), n.Minute())
}

func (e *Engine) addHistory(role, content string) {
	e.historyMu.Lock()
	defer e.historyMu.Unlock()

	e.history = append(e.history, openai.ChatCompletionMessage{
		Role:    role,
		Content: content,
	})

	maxLen := e.Config().Context.HistoryMaxLength
	if maxLen > 0 && len(e.history) > maxLen {
		e.history = e.history[len(e.history)-maxLen:]
	}
}

// matchMusicCommand 纯匹配：这句话是不是"裸指令"，是哪种。
// 不做任何副作用，可单独测试（音乐态判定树的全部规则都在这里）。
func matchMusicCommand(text string) (string, bool) {
	t := stripPunct(text)
	if t == "" {
		return "", false
	}
	bare := func(words ...string) bool {
		for _, w := range words {
			if isBareCommand(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case bare("下一首", "换一首", "切歌", "换首歌"):
		return "next", true
	case bare("停止", "停", "停下", "停一下", "别放", "不要放", "别唱了", "关掉音乐", "关音乐", "暂停"):
		return "stop", true
	}

	switch musicVolumeIntent(t) {
	case musicVolumeUp:
		return "volume_up", true
	case musicVolumeDown:
		return "volume_down", true
	case musicMuteOn:
		return "mute", true
	case musicMuteOff:
		return "unmute", true
	}
	return "", false
}

// execMusicCommand 执行匹配到的指令（副作用都在这边）。
func (e *Engine) execMusicCommand(action, text string) {
	switch action {
	case "next":
		if e.musicNext == nil {
			return
		}
		log.Printf("🎵 音乐指令：下一首")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if msg, err := e.musicNext(ctx); err == nil {
				log.Printf("🎵 %s", msg)
			} else {
				log.Printf("⚠️ 下一首失败: %v", err)
			}
		}()
	case "stop":
		if e.musicStop == nil {
			return
		}
		log.Printf("🎵 音乐指令：停止")
		e.musicStop()
	case "volume_up":
		log.Printf("🔊 音乐指令：大声（%q）", stripPunct(text))
		go e.adjustMusicVolume(musicVolumeStep)
	case "volume_down":
		log.Printf("🔊 音乐指令：小声（%q）", stripPunct(text))
		go e.adjustMusicVolume(-musicVolumeStep)
	case "mute":
		log.Printf("🔇 音乐指令：静音（%q）", stripPunct(text))
		go e.setMusicMuted(true)
	case "unmute":
		log.Printf("🔊 音乐指令：取消静音（%q）", stripPunct(text))
		go e.setMusicMuted(false)
	}
}

// handleMusicVoice 匹配并执行（保留给测试和旧调用点）。
func (e *Engine) handleMusicVoice(text string) (string, bool) {
	action, ok := matchMusicCommand(text)
	if !ok {
		return "", false
	}
	e.execMusicCommand(action, text)
	return action, true
}

// isBareCommand 判断 text 是否"基本就是这条指令"：
// 剥掉客套前缀/后缀后，除关键词外剩不下几个字。
func isBareCommand(text, keyword string) bool {
	idx := strings.Index(text, keyword)
	if idx < 0 {
		return false
	}
	rest := text[:idx] + text[idx+len(keyword):]
	for _, p := range musicCommandPrefixes {
		rest = strings.TrimPrefix(rest, p)
	}
	for _, suf := range musicCommandSuffixes {
		rest = strings.TrimSuffix(rest, suf)
	}
	return len([]rune(rest)) <= 1
}

var musicCommandPrefixes = []string{"帮我", "帮忙", "请", "麻烦", "给我", "再"}

var musicCommandSuffixes = []string{"一下", "播放", "音乐", "吧", "呀", "啊", "好吗", "谢谢"}

// musicStrongVerbs 明确的动作动词。没喊唤醒词但带这些词，说明是在指挥音箱
// （"帮换一首如愿"），交给路由器判；其余无唤醒词的话一律丢弃。
var musicStrongVerbs = []string{"换", "放", "听", "播", "来一首", "收藏"}

func containsAnyRune(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
