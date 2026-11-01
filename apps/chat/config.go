package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/cxjava/open-xiaoai/pkg/music"
	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// AuthUser 单用户凭证
type AuthUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// AuthConfig 多用户认证。users 为空则跳过验证
type AuthConfig struct {
	Users []AuthUser `yaml:"users"`
}

// InterruptConfig 打断配置：仅当关键词或唤醒词匹配时才打断
type InterruptConfig struct {
	Keywords     []string `yaml:"keywords"`      // 关键词列表，空则使用 call_ai_keywords
	MatchMode    string   `yaml:"match_mode"`    // exact, prefix, contains
	KwsInterrupt bool     `yaml:"kws_interrupt"` // 唤醒词(kws事件)也触发打断
}

// LLMConfig LLM API 配置，支持 OpenAI / Claude(OpenRouter) / DeepSeek 等
type LLMConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

type PromptConfig struct {
	System     string `yaml:"system"`
	InjectTime bool   `yaml:"inject_time"`
	Timezone   string `yaml:"timezone"`
}

type ContextConfig struct {
	HistoryMaxLength int `yaml:"history_max_length"`
}

type CustomReply struct {
	Match string `yaml:"match"`
	Text  string `yaml:"text,omitempty"`
	URL   string `yaml:"url,omitempty"`
}

// TTSConfig 语音合成配置。
// provider=xiaoai（默认）走音箱自带的 tts_play.sh；
// provider=doubao 走豆包网页版语音合成，音色更自然，音频由本服务中转给音箱播放。
type TTSConfig struct {
	Provider string `yaml:"provider"`
	// 豆包音色 ID，例如 zh_female_taozi_conversation_v4_wvae_bigtts
	Speaker string `yaml:"speaker"`
	// 音频格式：mp3（推荐）/ ogg_opus / pcm / wav
	Format     string `yaml:"format"`
	BitRate    int    `yaml:"bit_rate"`
	SampleRate int    `yaml:"sample_rate"`
	// 语速倍率，1.0 为正常，>1 更快
	SpeechRate float64 `yaml:"speech_rate"`
	// Emotion 情绪（部分音色支持）：happy / sad / angry / surprised / neutral，
	// 留空用音色默认情绪。实测同一句话不同情绪音频明显不同。
	Emotion string `yaml:"emotion"`
	// 音调，0 为正常，范围约 -1.0 ~ 1.0
	Pitch float64 `yaml:"pitch"`
}

func (c TTSConfig) UsesDoubao() bool {
	return strings.EqualFold(strings.TrimSpace(c.Provider), "doubao")
}

// Doubao2APIConfig 指向本机运行的 doubao2api 服务。
// 管理页通过它触发豆包扫码登录（doubao2api 负责保存 session 并注入它的浏览器）。
// base_url 留空则管理页不显示登录面板。
type Doubao2APIConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

func (c Doubao2APIConfig) Enabled() bool {
	return strings.TrimSpace(c.BaseURL) != ""
}

// DoubaoWebConfig 直连豆包网页版接口（不依赖 doubao2api / Playwright）。
// session_file 保存扫码得到的 cookie，落在本地磁盘。
type DoubaoWebConfig struct {
	Enabled     bool   `yaml:"enabled"`
	SessionFile string `yaml:"session_file"`
}

// SessionPath 返回登录态文件路径，未配置时给个默认值。
func (c DoubaoWebConfig) SessionPath() string {
	if p := strings.TrimSpace(c.SessionFile); p != "" {
		return p
	}
	return "doubao-session.json"
}

// ChatConfig 对话后端选择。
//
//	doubao_web = 豆包网页版直连（免费，但无法自定义提示词/风格）
//	llm        = OpenAI 兼容接口（DeepSeek/ARK/本地），可用 system prompt 控制风格
type ChatConfig struct {
	Provider string `yaml:"provider"`
}

// UseDoubaoWeb 是否走豆包网页版。
func (c ChatConfig) UseDoubaoWeb() bool {
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	return p == "" || p == "doubao_web" || p == "doubao"
}

// UseCodex 是否走 Codex CLI。
func (c ChatConfig) UseCodex() bool {
	return strings.EqualFold(strings.TrimSpace(c.Provider), "codex")
}

// ASRConfig 电脑端语音识别（服务端唤醒模式用）。
// Script 指 tools/asr-worker/asr_worker.py，Python 走装了 sherpa-onnx 的解释器。
type ASRConfig struct {
	Enabled bool `yaml:"enabled"`
	// Provider 旧字段（唤醒/对话共用）。新配置请用 wake_provider / dialog_provider。
	Provider string `yaml:"provider"`
	// WakeProvider 唤醒监听引擎：local（默认，SenseVoice + Paraformer 兜底）
	WakeProvider string `yaml:"wake_provider"`
	// DialogProvider 唤醒之后的对话识别引擎：
	//   doubao_stream = 火山引擎流式
	//   doubao        = 火山引擎整段（非流式）
	//   local         = 本地 SenseVoice
	//   local_alt     = 本地 SenseVoice + Paraformer 兜底
	DialogProvider string `yaml:"dialog_provider"`
	Python         string `yaml:"python"`
	Script         string `yaml:"script"`
	// ModelDir 本地 ASR 模型目录（传给 worker 的 ASR_MODEL_DIR）
	ModelDir string `yaml:"model_dir"`
	// AltModelDir 备用模型目录（Paraformer），只在唤醒词二次识别/对话兜底时用
	AltModelDir string `yaml:"alt_model_dir"`

	// 豆包语音识别（火山引擎 v3 大模型录音文件识别）凭据
	APIKey string `yaml:"api_key"`
	// ResourceID 整段识别的资源 id，默认 volc.seedasr.auc
	ResourceID string `yaml:"resource_id"`
	// StreamResourceID 流式识别的资源 id，默认 volc.bigasr.sauc.duration
	StreamResourceID string `yaml:"stream_resource_id"`
}

// WakeProviderName 唤醒监听用哪套识别；没配就用本地 SenseVoice。
func (c ASRConfig) WakeProviderName() string {
	if p := strings.ToLower(strings.TrimSpace(c.WakeProvider)); p != "" {
		return p
	}
	return "local"
}

// DialogProviderName 唤醒之后的对话识别用哪套。
// 兼容旧配置：只写了 provider 时，对话沿用它的值（local 会升级成 local_alt）。
func (c ASRConfig) DialogProviderName() string {
	if p := strings.ToLower(strings.TrimSpace(c.DialogProvider)); p != "" {
		return p
	}
	switch p := strings.ToLower(strings.TrimSpace(c.Provider)); p {
	case "doubao", "doubao_stream":
		return p
	case "local", "local_alt":
		if strings.TrimSpace(c.AltModelDir) != "" {
			return "local_alt"
		}
		return "local"
	}
	if strings.TrimSpace(c.AltModelDir) != "" {
		return "local_alt"
	}
	return "local"
}

// CORSConfig 跨域配置。前端单独部署时用于放行管理 API。
// allowed_origins 为空表示允许任意来源（认证走 Authorization 头，不使用 Cookie）。
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// ScheduleConfig 定时任务结果播报：轮询豆包任务，把新结果播到音箱。
// 任务本身由用户在豆包网页/App 里创建，服务端只读。
type ScheduleConfig struct {
	Enabled      bool   `yaml:"enabled"`
	PollInterval string `yaml:"poll_interval"` // 轮询间隔，如 60s（最小 10s）
	QuietHours   string `yaml:"quiet_hours"`   // 静默时段，如 23:00-07:00；留空表示全天播报
}

// SearchConfig 联网搜索配置。
// api_key 为空时自动从 codex_home 的 config.toml 里读 DeepSeek key。
type SearchConfig struct {
	APIKey string `yaml:"api_key"`
	Count  int    `yaml:"count"`
}

// HomeAssistantConfig 走 Home Assistant 控制家电（替代直连米家）。
// base_url + token 都填了才启用；token 在 HA「个人资料 → 长期访问令牌」里创建。
type HomeAssistantConfig struct {
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
}

// Enabled 是否配置完整。
func (c HomeAssistantConfig) Enabled() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.Token) != ""
}

// MusicRouterConfig 音乐态的语义路由（L4）。
// 白名单/唤醒词/歌词比对都没拦住的句子才交给模型判断意图。
type MusicRouterConfig struct {
	Enabled bool `yaml:"enabled"`
	// Engine 路由用哪个：
	//   llm   = 轻量分类器优先（0.9s），失败才升级到 codex 线程（默认）
	//   codex = 全部交给 codex 独立线程（有真工具，但一轮约 4s）
	Engine string `yaml:"engine"`
	// Model 留空则用 llm.model。
	Model string `yaml:"model"`
	// TimeoutMs 单次判断的超时，超时按"忽略"处理。0 用默认 1500ms。
	TimeoutMs int `yaml:"timeout_ms"`
	// MinConfidence 低于这个置信度不执行任何动作。0 用默认 0.6。
	MinConfidence float64 `yaml:"min_confidence"`
	// Prompt 路由线程的系统提示词；留空用内置默认。改完要在「智能体」页对新会话生效。
	Prompt string `yaml:"prompt"`
}

// MusicDuckConfig 放音乐时自动压低音量。
// 没有 AEC 的麦克风在音乐大声时会被掩蔽+削波，说话声整个被盖住——这是物理层问题，
// 事后做分离/降噪都救不回来，只能先把音乐本身压小，让 ASR 听得清指令。
type MusicDuckConfig struct {
	Enabled bool `yaml:"enabled"`
	// Volume 起播后把音量压到这个值（0-100）；<=0 用默认值 35。
	// 停播（下一首/停止/自然结束）时还原成用户原来的音量。
	Volume int `yaml:"volume"`
}

// NativeWakeConfig 原生唤醒接管（音乐态）。
//
// 音乐大声时只有原生 KWS 听得见「小爱同学」，但它会让小爱继续回答。
// 打开这个开关后，原生 ASR 出 final 时冻结设备播放器（原生 TTS 和音乐一起停），
// 由豆包接管这一轮对话，播完再解冻。详见 docs/device-audio-aec-native-asr.md。
type NativeWakeConfig struct {
	Enabled bool `yaml:"takeover_enabled"`
	// Mode 接管方式：freeze=冻结播放器（默认）；speech_host=接管 speech.usock。
	// 真正决定模式的是 client 的 -speech-host 参数，这里只用于文档与启动检查。
	Mode string `yaml:"mode"`
	// MaxSeconds 单轮接管的最长时长（秒），超时强制解冻，避免音乐永久停住。<=0 用默认 25。
	MaxSeconds int `yaml:"max_seconds"`
}

type AppConfig struct {
	Server         ServerConfig    `yaml:"server"`
	Auth           AuthConfig      `yaml:"auth"`
	CORS           CORSConfig      `yaml:"cors"`
	Proxy          string          `yaml:"proxy"` // HTTP/SOCKS5 代理，如 http://127.0.0.1:7890
	LLM            LLMConfig       `yaml:"llm"`
	Prompt         PromptConfig    `yaml:"prompt"`
	Context        ContextConfig   `yaml:"context"`
	Interrupt      InterruptConfig `yaml:"interrupt"`
	CallAIKeywords []string        `yaml:"call_ai_keywords"`
	// NativeKeywords 命中则完全不接管，留给小爱原生处理（家电控制等）。
	// 只在 call_ai_keywords 为空（全量进 AI）时才有意义，优先级高于 call_ai_keywords。
	NativeKeywords []string      `yaml:"native_keywords"`
	CustomReplies  []CustomReply `yaml:"custom_replies"`
	Greeting       string        `yaml:"greeting"`
	ErrorMessage   string        `yaml:"error_message"`
	// ReplyPrefix 在 AI 第一句 TTS 之前播一段固定前缀，用于让用户区分
	// "这是 AI 在说" vs "这是小爱原生回复 / 音乐 / 系统提示音"。
	// 空字符串 = 关闭该前缀。配置文件未设置时使用默认值（见 defaultConfig）。
	// 注意：只在通过 LLM 走完整流程的 AI 回复前插入；自定义回复(custom_replies)、
	// 错误回退提示(error_message) 都不挂前缀——那些不是 AI 真正生成的内容。
	ReplyPrefix string `yaml:"reply_prefix"`
	// ContinuationSeconds 每轮回答结束后，对话窗口保持多久（秒）。
	// 窗口内说话不用再喊唤醒词；超时后需要重新唤醒。0 表示用默认值。
	ContinuationSeconds int                 `yaml:"continuation_seconds"`
	TTS                 TTSConfig           `yaml:"tts"`
	Schedules           ScheduleConfig      `yaml:"schedules"`
	Doubao2API          Doubao2APIConfig    `yaml:"doubao2api"`
	Chat                ChatConfig          `yaml:"chat"`
	Codex               CodexConfig         `yaml:"codex"`
	DoubaoWeb           DoubaoWebConfig     `yaml:"doubao_web"`
	ASR                 ASRConfig           `yaml:"asr"`
	Music               music.MusicConfig   `yaml:"music"`
	MusicDuck           MusicDuckConfig     `yaml:"music_duck"`
	NativeWake          NativeWakeConfig    `yaml:"native_wake"`
	MusicRouter         MusicRouterConfig   `yaml:"music_router"`
	Search              SearchConfig        `yaml:"search"`
	HomeAssistant       HomeAssistantConfig `yaml:"homeassistant"`
}

var defaultInterruptKeywords = []string{"闭嘴", "停止", "暂停", "停一下", "不要说了", "别说了"}

type instructionDecision int

const (
	instructionDecisionIgnore instructionDecision = iota
	instructionDecisionInterruptOnly
	instructionDecisionCallAI
)

// GetLLM 返回 LLM 配置
func (c *AppConfig) GetLLM() LLMConfig {
	return c.LLM
}

func defaultConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 4399,
		},
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			Model:   "gpt-4.1-mini",
		},
		Prompt:  PromptConfig{InjectTime: true, Timezone: "Asia/Shanghai"},
		Context: ContextConfig{HistoryMaxLength: 10},
		Interrupt: InterruptConfig{
			Keywords:     append([]string(nil), defaultInterruptKeywords...),
			MatchMode:    "prefix",
			KwsInterrupt: true,
		},
		CallAIKeywords: []string{"请", "你"},
		Greeting:       "已连接",
		ErrorMessage:   "出错了，请稍后再试吧！",
		ReplyPrefix:    "AI回复",
		TTS: TTSConfig{
			Provider:   "xiaoai",
			Speaker:    defaultDoubaoSpeaker,
			Format:     defaultDoubaoFormat,
			BitRate:    defaultDoubaoBitRate,
			SampleRate: defaultDoubaoSampleRate,
			SpeechRate: defaultDoubaoSpeed,
		},
		Doubao2API: Doubao2APIConfig{
			BaseURL: "http://127.0.0.1:9090",
		},
		Schedules: ScheduleConfig{
			PollInterval: "60s",
			QuietHours:   "23:00-07:00",
		},
		MusicDuck: MusicDuckConfig{
			Enabled: true,
			Volume:  defaultMusicDuckVolume,
		},
		NativeWake: NativeWakeConfig{
			Enabled:    false, // 默认关闭，验收后再打开
			Mode:       "freeze",
			MaxSeconds: 25,
		},
		MusicRouter: MusicRouterConfig{
			Enabled:       true,
			Engine:        "llm",
			TimeoutMs:     1500,
			MinConfidence: 0.6,
		},
	}
}

func loadConfig(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	return loadConfigFromBytes(data)
}

// expandEnv 把配置里的 ${VAR} / $VAR 替换成环境变量值。
// 密钥类字段建议这么写，避免明文进 git。
func expandEnv(data []byte) []byte {
	return []byte(os.Expand(string(data), func(k string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		return "${" + k + "}"
	}))
}

func loadConfigFromBytes(data []byte) (*AppConfig, error) {
	data = expandEnv(data)
	cfg := defaultConfig()

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// RequiresAuth 是否启用认证（users 非空）
func (c *AuthConfig) RequiresAuth() bool {
	return len(c.Users) > 0
}

// ValidateAuth 校验用户名密码
func (c *AuthConfig) ValidateAuth(username, password string) bool {
	for _, u := range c.Users {
		if u.Username == username && u.Password == password {
			return true
		}
	}
	return false
}

func (c *AppConfig) shouldCallAI(text string) bool {
	return c.callAIKeyword(text) != ""
}

func (c *AppConfig) callAIKeyword(text string) string {
	if len(c.CallAIKeywords) == 0 {
		return "*"
	}
	for _, kw := range c.CallAIKeywords {
		if strings.HasPrefix(text, kw) {
			return kw
		}
	}
	return ""
}

// IsBareTrigger 判断这句话是不是"只喊了触发词"（如「豆包」「豆包豆包」），没有实际内容。
// 这种句子不该当问题发给 AI，而应进入对话模式等下一句。
func (c *AppConfig) IsBareTrigger(text string) bool {
	if len(c.CallAIKeywords) == 0 {
		return false
	}
	rest := text
	for _, kw := range c.CallAIKeywords {
		if kw == "" {
			continue
		}
		rest = strings.ReplaceAll(rest, kw, "")
	}
	rest = strings.Trim(rest, " \t\r\n，。！？、,.!?~～…—")
	return rest == ""
}

// 唤醒词表：识别文本里"包含"任一即算命中。
// 除了正经叫法，还塞了一批常见听错的形式（豆康/豆帮/豆宝…），
// 因为通用 ASR 对"豆包"这种专有名词并不敏感。
var defaultWakeWords = []string{
	"豆包", "豆包豆包", "豆包助手", "你好豆包",
	"逗包", "逗包逗包",
	"豆宝", "豆报", "豆康", "豆帮", "豆邦", "豆胞", "豆暴", "多包", "多宝",
	"的帮", "杜邦", "怎宝", "导当", "等么的吧",
	"豆吧", "豆吧豆吧", "多吧多吧", "逗吧逗吧",
	"淘宝淘宝", "读吧读吧", "图吧图吧",
	"杜啊", "杜博", "杜啊杜啊", "杜博杜博", "堵啊堵啊",
	"小度", "小度小度", "小豆", "小豆豆",
}

// 模糊匹配：容忍「豆X豆X」「豆X包/宝/报」这类结构。
var wakeFuzzyPatterns = []*regexp.Regexp{
	// 只接受"豆/逗 + 一个像 bao 的字"和"两个豆字"，避免「豆沙包」「都包括」误唤醒
	regexp.MustCompile(`^[豆逗多].{0,3}[豆逗多]`), // 豆包豆包 / 豆康豆吧
	regexp.MustCompile(`^[豆逗多][包宝报暴邦帮吧巴康]`), // 豆康 / 豆宝 / 豆吧
	regexp.MustCompile(`^小[度杜杜]`),           // 小度
}

// weakWakeWords 是常用词，只能"整句等于它"时才算唤醒。
// 否则「打开淘宝」「你读吧」这类日常话会误触发。
var weakWakeWords = []string{
	"淘宝", "读吧", "图吧", "堵啊", "都包", "兜包",
}

// isWholeWordMatch 判断整句是否就等于某个词（忽略标点）。
func isWholeWordMatch(text string, words []string) bool {
	t := strings.Trim(text, "，。！？、,.!?~～…— \t\r\n")
	for _, w := range words {
		if t == w {
			return true
		}
	}
	return false
}

// IsAIWakeWord 判断识别文本里是否喊了唤醒词。
// stripPunct 去掉首尾标点与空白，匹配前统一用它规范化。
func stripPunct(s string) string {
	const cut = " \t\r\n，。！？、,.!?~～…—：:;；\"'“”‘’()（）[]【】「」《》〈〉〔〕"
	return strings.Trim(s, cut)
}

func (c *AppConfig) IsAIWakeWord(raw string) bool {
	kw := stripPunct(raw)
	if kw == "" || kw == "__STARTED__" {
		return false
	}
	for _, k := range c.CallAIKeywords {
		k = strings.TrimSpace(k)
		if k != "" && strings.Contains(kw, k) {
			return true
		}
	}
	for _, k := range defaultWakeWords {
		if strings.Contains(kw, k) {
			return true
		}
	}
	if isWholeWordMatch(kw, weakWakeWords) {
		return true
	}
	for _, re := range wakeFuzzyPatterns {
		if re.MatchString(kw) {
			return true
		}
	}
	// 最后兜底：按拼音音节算相似度（覆盖"字形完全不同但读音接近"的听错）
	return matchWakePinyin(kw)
}

// nativeKeyword 判断这句话是否应该留给小爱原生处理。
func (c *AppConfig) nativeKeyword(text string) string {
	for _, kw := range c.NativeKeywords {
		if kw != "" && strings.Contains(text, kw) {
			return kw
		}
	}
	return ""
}

func (c *AppConfig) instructionDecision(text string) (instructionDecision, string) {
	if c.hasCustomReply(text) {
		return instructionDecisionCallAI, "custom_reply"
	}
	// 家电控制这类指令交给小爱原生处理：被 AI 接管只会得到一句反问。
	if kw := c.nativeKeyword(text); kw != "" {
		return instructionDecisionIgnore, "native:" + kw
	}
	if kw := c.callAIKeyword(text); kw != "" {
		return instructionDecisionCallAI, kw
	}
	if kw := c.interruptKeyword(text); kw != "" {
		return instructionDecisionInterruptOnly, kw
	}
	return instructionDecisionIgnore, ""
}

func (c *AppConfig) hasCustomReply(text string) bool {
	for _, r := range c.CustomReplies {
		if r.Match == text {
			return true
		}
	}
	return false
}

func (c *AppConfig) shouldStopTTSBeforeHandling(text string) bool {
	return c.hasCustomReply(text)
}

// ShouldInterrupt 是否应打断：instruction 文本匹配关键词时返回 true
func (c *AppConfig) ShouldInterrupt(userText string) bool {
	return c.interruptKeyword(userText) != ""
}

func (c *AppConfig) interruptKeyword(userText string) string {
	keywords := append([]string(nil), c.Interrupt.Keywords...)
	keywords = append(keywords, c.CallAIKeywords...)
	if len(keywords) == 0 {
		return ""
	}
	text := strings.TrimSpace(userText)
	if text == "" {
		return ""
	}
	mode := strings.ToLower(c.Interrupt.MatchMode)
	if mode == "" {
		mode = "prefix"
	}
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		switch mode {
		case "exact":
			if text == kw {
				return kw
			}
		case "prefix":
			if strings.HasPrefix(text, kw) {
				return kw
			}
		case "contains":
			if strings.Contains(text, kw) {
				return kw
			}
		default:
			if strings.HasPrefix(text, kw) {
				return kw
			}
		}
	}
	return ""
}
