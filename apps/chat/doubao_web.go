package main

// 豆包网页版（www.doubao.com）原生客户端：扫码登录 + /chat/completion 流式对话。
//
// 为什么不走 doubao2api：那边需要 Python + Playwright 常驻浏览器，部署重。
// 这里只依赖 Go 标准库，纯 HTTP 直连网页接口：
//
//	登录：GET / → /passport/safe/csrf_token → /passport/web/get_qrcode
//	      → 轮询 /passport/web/check_qrconnect → 跟随 redirect 拿 sessionid
//	对话：POST /chat/completion?<security_params>，SSE 流式返回
//
// 注意：/chat/completion 不需要 a_bogus 签名（那是浏览器 fetch hook 给页面内请求加的），
// 只要带齐 cookie + x-tt-passport-csrf-token 即可。msToken 有就用、没有就不传，
// 传假值反而会触发风控 710022002。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	doubaoBaseURL = "https://www.doubao.com"

	// passport 侧用 497858，聊天接口用 582478，两者不能混用
	doubaoPassportAID = "497858"
	doubaoSecurityAID = "582478"

	doubaoDefaultBotID = "7338286299411103781"

	doubaoChromiumBuild = "148.0.7816.0"
	doubaoUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

	doubaoQRPollInterval = 1500 * time.Millisecond
	doubaoQRTimeout      = 120 * time.Second
)

// 触发风控/验证码的错误码
var doubaoRiskErrorCodes = map[int]bool{710022002: true, 710022004: true}

// 登录态过期
var errDoubaoNotLoggedIn = errors.New("豆包未登录或登录已过期")

type doubaoSession struct {
	Cookies  map[string]string `json:"cookies"`
	FP       string            `json:"fp"`
	DeviceID string            `json:"device_id"`
	WebID    string            `json:"web_id"`
	TeaUUID  string            `json:"tea_uuid"`
	SavedAt  int64             `json:"saved_at"`

	// 当前对话上下文，重启后接着用同一个会话，避免每次重启都新建
	ConvID    string `json:"conv_id"`
	SectionID string `json:"section_id"`
	MsgIndex  int    `json:"msg_index"`
}

// DoubaoWeb 是豆包网页接口的客户端，可安全并发使用。
type DoubaoWeb struct {
	mu      sync.Mutex
	http    *http.Client
	path    string
	sess    doubaoSession
	lastErr string

	// 当前会话：从 SSE_ACK 里拿到，下一轮带上就不再新建会话
	convMu    sync.Mutex
	convID    string
	sectionID string
	msgIndex  int

	qrMu      sync.Mutex
	qrToken   string
	qrCSRF    string
	qrCookies map[string]string
	qrStarted time.Time

	// 调试：抓取原始 SSE 行（仅 ChatCaptureRaw 期间非空）
	rawMu  sync.Mutex
	rawBuf *bytes.Buffer
}

// NewDoubaoWeb 创建客户端并从 statePath 恢复登录态（文件不存在不算错误）。
func NewDoubaoWeb(statePath string) *DoubaoWeb {
	if statePath == "" {
		statePath = "doubao-session.json"
	}
	d := &DoubaoWeb{
		path: statePath,
		http: &http.Client{
			Timeout: 0, // SSE 需要长连接，靠 ctx 控制
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
		sess: doubaoSession{Cookies: map[string]string{}},
	}
	d.load()
	return d
}

func (d *DoubaoWeb) load() {
	raw, err := os.ReadFile(d.path)
	if err != nil {
		return
	}
	var sess doubaoSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return
	}
	if sess.Cookies == nil {
		sess.Cookies = map[string]string{}
	}
	d.sess = sess
	d.convID = sess.ConvID
	d.sectionID = sess.SectionID
	d.msgIndex = sess.MsgIndex
}

func (d *DoubaoWeb) save() error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil && filepath.Dir(d.path) != "." {
		return err
	}
	raw, err := json.MarshalIndent(d.sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.path, raw, 0o600)
}

// LoggedIn 报告是否已持有 sessionid。
func (d *DoubaoWeb) LoggedIn() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sess.Cookies["sessionid"] != ""
}

// Status 返回给管理台的简要状态。
func (d *DoubaoWeb) Status() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]any{
		"logged_in":  d.sess.Cookies["sessionid"] != "",
		"cookies":    len(d.sess.Cookies),
		"session_id": maskSecret(d.sess.Cookies["sessionid"]),
	}
	if d.sess.SavedAt > 0 {
		out["saved_at"] = time.Unix(d.sess.SavedAt, 0).Format(time.RFC3339)
	}
	if d.lastErr != "" {
		out["last_error"] = d.lastErr
	}
	return out
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		if s == "" {
			return ""
		}
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

// ---------------- HTTP 基础 ----------------

func (d *DoubaoWeb) cookieHeader(cookies map[string]string) string {
	parts := make([]string, 0, len(cookies))
	for k, v := range cookies {
		if v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func parseSetCookie(h http.Header, into map[string]string) {
	for _, raw := range h.Values("Set-Cookie") {
		part := raw
		if i := strings.Index(part, ";"); i >= 0 {
			part = part[:i]
		}
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		into[k] = strings.TrimSpace(v)
	}
}

// doGet 执行 GET，可选择性跟随重定向（手动跟随以便收集每一跳的 Set-Cookie）。
func (d *DoubaoWeb) doGet(ctx context.Context, rawURL string, headers map[string]string, cookies map[string]string, follow bool) (int, string, map[string]string, error) {
	collected := map[string]string{}
	current := rawURL

	client := *d.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	for hop := 0; hop < 10; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return 0, "", collected, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		merged := map[string]string{}
		for k, v := range cookies {
			merged[k] = v
		}
		for k, v := range collected {
			merged[k] = v
		}
		if ck := d.cookieHeader(merged); ck != "" {
			req.Header.Set("Cookie", ck)
		}

		resp, err := client.Do(req)
		if err != nil {
			return 0, "", collected, err
		}
		parseSetCookie(resp.Header, collected)
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return resp.StatusCode, "", collected, readErr
		}

		if follow && resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			if loc != "" {
				if !strings.HasPrefix(loc, "http") {
					base, _ := url.Parse(current)
					ref, err := url.Parse(loc)
					if err == nil && base != nil {
						loc = base.ResolveReference(ref).String()
					}
				}
				current = loc
				continue
			}
		}
		return resp.StatusCode, string(body), collected, nil
	}
	return 0, "", collected, errors.New("重定向次数过多")
}

func doubaoBaseHeaders() map[string]string {
	return map[string]string{
		"User-Agent": doubaoUserAgent,
		"Accept":     "application/json, text/plain, */*",
		"Referer":    "https://www.doubao.com/chat/login",
		"Origin":     "https://www.doubao.com",
	}
}

// ---------------- 扫码登录 ----------------

func randDigits(n int) string {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		return strings.Repeat("1", n)
	}
	s := v.String()
	for len(s) < n {
		s = "0" + s
	}
	return s
}

const randLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(randLetters))))
		if err != nil {
			b[i] = 'a'
			continue
		}
		b[i] = randLetters[idx.Int64()]
	}
	return string(b)
}

// generateDeviceParams 每次登录都换一套设备指纹，避免继承上一次的风险分。
func generateDeviceParams() (fp, deviceID, webID string) {
	fp = "verify_" + randString(8) + "_" + randString(4) + "_" + randString(4) + "_" + randString(4) + "_" + randString(12)
	deviceID = "7" + randDigits(15)
	webID = "76" + randDigits(17)
	return
}

// StartQR 取一张新的登录二维码，返回 PNG 的 base64（不含 data: 前缀）。
func (d *DoubaoWeb) StartQR(ctx context.Context) (string, error) {
	cookies := map[string]string{}

	// 1) 访问首页，拿 ttwid 等基础 cookie
	_, _, newCK, err := d.doGet(ctx, doubaoBaseURL+"/", doubaoBaseHeaders(), cookies, false)
	if err != nil {
		return "", fmt.Errorf("访问豆包首页失败: %w", err)
	}
	for k, v := range newCK {
		cookies[k] = v
	}

	// 2) CSRF token
	csrf := cookies["passport_csrf_token"]
	if csrf == "" {
		csrf = cookies["passport_csrf_token_default"]
	}
	if csrf == "" {
		u := fmt.Sprintf("%s/passport/safe/csrf_token/?aid=%s", doubaoBaseURL, doubaoPassportAID)
		_, body, newCK, err := d.doGet(ctx, u, doubaoBaseHeaders(), cookies, false)
		if err == nil {
			for k, v := range newCK {
				cookies[k] = v
			}
			var parsed struct {
				ErrorCode int `json:"error_code"`
				Data      struct {
					PassportCSRF string `json:"passport_csrf_token"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(body), &parsed) == nil && parsed.ErrorCode == 0 && parsed.Data.PassportCSRF != "" {
				csrf = parsed.Data.PassportCSRF
			}
		}
	}
	if csrf == "" {
		csrf = cookies["passport_csrf_token"]
	}

	// 3) 取二维码
	next := url.QueryEscape("https://www.doubao.com")
	u := fmt.Sprintf("%s/passport/web/get_qrcode/?next=%s&aid=%s", doubaoBaseURL, next, doubaoPassportAID)
	h := doubaoBaseHeaders()
	h["x-tt-passport-csrf-token"] = csrf

	_, body, newCK, err := d.doGet(ctx, u, h, cookies, false)
	if err != nil {
		return "", fmt.Errorf("获取二维码失败: %w", err)
	}
	for k, v := range newCK {
		cookies[k] = v
	}

	var parsed struct {
		Message string `json:"message"`
		Data    struct {
			ErrorCode int    `json:"error_code"`
			Token     string `json:"token"`
			QRCode    string `json:"qrcode"`
			QRCodeURL string `json:"qrcode_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "", fmt.Errorf("二维码响应解析失败: %w (body=%.200s)", err, body)
	}
	if parsed.Data.ErrorCode != 0 || (parsed.Message != "" && parsed.Message != "success") {
		return "", fmt.Errorf("获取二维码失败: error_code=%d message=%s", parsed.Data.ErrorCode, parsed.Message)
	}
	if parsed.Data.Token == "" {
		return "", errors.New("二维码响应中没有 token")
	}

	qrRaw := parsed.Data.QRCode
	if qrRaw == "" {
		qrRaw = parsed.Data.QRCodeURL
	}
	if qrRaw == "" {
		return "", errors.New("二维码响应中没有图片数据")
	}
	if strings.HasPrefix(qrRaw, "data:") {
		if _, rest, ok := strings.Cut(qrRaw, ","); ok {
			qrRaw = rest
		}
	}

	d.qrMu.Lock()
	d.qrToken = parsed.Data.Token
	d.qrCSRF = csrf
	d.qrCookies = cookies
	d.qrStarted = time.Now()
	d.qrMu.Unlock()

	return qrRaw, nil
}

// PollQR 轮询一次扫码状态。返回 pending / scanned / confirmed / expired。
func (d *DoubaoWeb) PollQR(ctx context.Context) (string, error) {
	d.qrMu.Lock()
	token, csrf, cookies := d.qrToken, d.qrCSRF, d.qrCookies
	started := d.qrStarted
	d.qrMu.Unlock()

	if token == "" {
		return "", errors.New("还没有发起扫码登录")
	}
	if time.Since(started) > doubaoQRTimeout {
		return "expired", nil
	}

	next := url.QueryEscape("https://www.doubao.com")
	u := fmt.Sprintf("%s/passport/web/check_qrconnect/?next=%s&token=%s&aid=%s",
		doubaoBaseURL, next, url.QueryEscape(token), doubaoPassportAID)
	h := doubaoBaseHeaders()
	h["x-tt-passport-csrf-token"] = csrf

	_, body, newCK, err := d.doGet(ctx, u, h, cookies, false)
	if err != nil {
		return "pending", nil // 网络抖动不致命，继续轮询
	}
	for k, v := range newCK {
		cookies[k] = v
	}

	var parsed struct {
		Message string `json:"message"`
		Data    struct {
			ErrorCode   int    `json:"error_code"`
			Status      string `json:"status"`
			Description string `json:"description"`
			RedirectURL string `json:"redirect_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "pending", nil
	}
	if parsed.Data.ErrorCode != 0 {
		desc := strings.ToLower(parsed.Data.Description)
		if strings.Contains(desc, "expired") || strings.Contains(desc, "过期") {
			return "expired", nil
		}
		return "pending", nil
	}

	switch parsed.Data.Status {
	case "new", "":
		return "pending", nil
	case "scanned":
		return "scanned", nil
	case "expired":
		return "expired", nil
	case "confirmed":
		final := cookies
		if parsed.Data.RedirectURL != "" {
			_, _, redirectCK, err := d.doGet(ctx, parsed.Data.RedirectURL, doubaoBaseHeaders(), cookies, true)
			if err == nil {
				for k, v := range redirectCK {
					final[k] = v
				}
			}
		}

		fp, deviceID, webID := generateDeviceParams()

		d.mu.Lock()
		d.sess.Cookies = final
		d.sess.FP = fp
		d.sess.DeviceID = deviceID
		d.sess.WebID = webID
		d.sess.SavedAt = time.Now().Unix()
		saveErr := d.save()
		d.mu.Unlock()

		d.qrMu.Lock()
		d.qrToken = ""
		d.qrMu.Unlock()

		if final["sessionid"] == "" {
			return "confirmed", errors.New("登录已确认，但没有拿到 sessionid")
		}
		if saveErr != nil {
			return "confirmed", fmt.Errorf("登录成功但保存失败: %w", saveErr)
		}
		return "confirmed", nil
	}
	return "pending", nil
}

// ---------------- 会话状态 ----------------

func (d *DoubaoWeb) currentConvID() string {
	d.convMu.Lock()
	defer d.convMu.Unlock()
	return d.convID
}

func (d *DoubaoWeb) currentConv() (convID, sectionID string) {
	d.convMu.Lock()
	defer d.convMu.Unlock()
	return d.convID, d.sectionID
}

func (d *DoubaoWeb) setConv(convID, sectionID string) {
	if convID == "" {
		return
	}
	d.convMu.Lock()
	d.convID = convID
	if sectionID != "" {
		d.sectionID = sectionID
	}
	conv, section, idx := d.convID, d.sectionID, d.msgIndex
	d.convMu.Unlock()
	d.persistConv(conv, section, idx)
}

// persistConv 把当前会话写进 session 文件，重启后继续用。
func (d *DoubaoWeb) persistConv(convID, sectionID string, msgIndex int) {
	d.mu.Lock()
	d.sess.ConvID = convID
	d.sess.SectionID = sectionID
	d.sess.MsgIndex = msgIndex
	if d.sess.Cookies["sessionid"] != "" {
		_ = d.save()
	}
	d.mu.Unlock()
}

// ResetConversation 丢弃当前会话，下一句话会新建一个。
func (d *DoubaoWeb) ResetConversation() {
	d.convMu.Lock()
	d.convID = ""
	d.sectionID = ""
	d.msgIndex = 0
	d.convMu.Unlock()
	d.persistConv("", "", 0)
}

// bumpMessageIndex 一轮问答在会话里新增 2 条消息（用户 + 助手）。
func (d *DoubaoWeb) bumpMessageIndex() {
	d.convMu.Lock()
	d.msgIndex += 2
	conv, section, idx := d.convID, d.sectionID, d.msgIndex
	d.convMu.Unlock()
	d.persistConv(conv, section, idx)
}

// HasConversation 报告当前是否已有一个正在延续的豆包会话。
// 有会话时豆包服务端自己保存上下文，不需要我们重发历史和系统设定。
func (d *DoubaoWeb) HasConversation() bool {
	conv, _ := d.currentConv()
	return conv != ""
}

// ConversationInfo 给管理台展示用。
func (d *DoubaoWeb) ConversationInfo() map[string]any {
	convID, _ := d.currentConv()
	return map[string]any{
		"conversation_id": convID,
		"persistent":      convID != "",
	}
}

// ---------------- 对话 ----------------

func (d *DoubaoWeb) securityParams() map[string]string {
	d.mu.Lock()
	fp, deviceID, webID := d.sess.FP, d.sess.DeviceID, d.sess.WebID
	msToken := d.sess.Cookies["msToken"]
	d.mu.Unlock()

	if fp == "" {
		fp, deviceID, webID = generateDeviceParams()
	}

	params := map[string]string{
		"aid":                 doubaoSecurityAID,
		"real_aid":            doubaoSecurityAID,
		"device_id":           deviceID,
		"tea_uuid":            deviceID,
		"web_id":              webID,
		"device_platform":     "web",
		"language":            "zh",
		"region":              "CN",
		"sys_region":          "CN",
		"pkg_type":            "release_version",
		"version_code":        "20800",
		"pc_version":          "2.1.7",
		"chromium_version":    doubaoChromiumBuild,
		"client_platform":     "pc_client",
		"runtime":             "web",
		"runtime_version":     "3.5.4",
		"samantha_web":        "1",
		"use-olympus-account": "1",
		"fp":                  fp,
		"web_tab_id":          newUUID(),
	}
	if msToken != "" {
		params["msToken"] = msToken
	}
	return params
}

func (d *DoubaoWeb) completionPayload(text string) map[string]any {
	convID, sectionID := d.currentConv()
	d.convMu.Lock()
	msgIndex := d.msgIndex
	d.convMu.Unlock()

	return map[string]any{
		"client_meta": map[string]any{
			"local_conversation_id": "local_" + randString(16),
			"conversation_id":       convID,
			"bot_id":                doubaoDefaultBotID,
			"last_section_id":       sectionID,
			"last_message_index":    msgIndex,
		},
		"messages": []any{
			map[string]any{
				"local_message_id": newUUID(),
				"content_block": []any{
					map[string]any{
						"block_type": 10000,
						"content": map[string]any{
							"text_block": map[string]any{
								"text":          text,
								"icon_url":      "",
								"icon_url_dark": "",
								"summary":       "",
							},
							"pc_event_block": "",
						},
						"block_id":      newUUID(),
						"parent_id":     "",
						"meta_info":     []any{},
						"append_fields": []any{},
						"is_finish":     true,
						"patch_type":    2,
					},
				},
				"message_status": 0,
			},
		},
		"option": map[string]any{
			"send_message_scene":       "",
			"create_time_ms":           0,
			"collect_id":               "",
			"is_audio":                 false,
			"answer_with_suggest":      true,
			"tts_switch":               false,
			"need_deep_think":          0,
			"click_clear_context":      false,
			"from_suggest":             false,
			"is_regen":                 false,
			"is_replace":               false,
			"disable_sse_cache":        false,
			"select_text_action":       "",
			"resend_for_regen":         false,
			"scene_type":               0,
			"unique_key":               newUUID(),
			"start_seq":                0,
			"need_create_conversation": convID == "",
			"conversation_init_option": map[string]any{"need_ack_conversation": true},
			"regen_query_id":           []any{},
			"edit_query_id":            []any{},
			"regen_instruction":        "",
			"no_replace_for_regen":     false,
			"message_from":             0,
			"shared_app_name":          "",
			"action_bar_skill_id":      0,
			"sse_recv_event_options":   map[string]any{"support_chunk_delta": true},
			"is_ai_playground":         false,
			// agent_mode=2 打开智能体能力（含联网搜索工具），网页版请求里带的
			"agent_mode": 2,
			"model_config": map[string]any{
				"model_item_key":     "0",
				"reasoning_effort":   3,
				"model_extra_params": map[string]any{},
			},
			"aggregate_params": map[string]any{
				"agent_mode":          "2",
				"conversation_mode":   "",
				"mention_ext":         "[{}]",
				"mention_plugin_list": "[]",
				"mention_skill_list":  "[]",
				"mode_id":             "1",
				"model_item_key":      "0",
				"provider_id":         "",
				"reasoning_effort":    "3",
			},
			"general_task_param": map[string]any{
				"runtime_type":      0,
				"agent_task_param":  map[string]any{},
				"project_id":        "",
				"server_do_compare": true,
			},
		},
		"chat_ability": map[string]any{},
		"ext": map[string]any{
			"use_deep_think":                "0",
			"fp":                            d.currentFP(),
			"use_submit_pipeline":           "1",
			"commerce_credit_config_enable": "0",
			"sub_conv_firstmet_type":        "1",
			"agent_mode":                    "2",
		},
	}
}

func (d *DoubaoWeb) currentFP() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sess.FP == "" {
		return "verify_default_default_default_default_default"
	}
	return d.sess.FP
}

// ChatStream 发送一条用户消息，逐段回调文本增量。返回完整回答。
//
// 复用上一次拿到的 conversation_id；若该会话已失效（豆包侧删除/过期），
// 清掉会话重开一次。已经吐出文字的情况下不重试，避免重复播报。
func (d *DoubaoWeb) ChatStream(ctx context.Context, text string, onDelta func(string)) (string, error) {
	convID, _ := d.currentConv()
	emitted := false
	wrapped := func(delta string) {
		emitted = true
		if onDelta != nil {
			onDelta(delta)
		}
	}
	ans, err := d.chatStreamOnce(ctx, text, wrapped)
	if err != nil && convID != "" && !emitted && ctx.Err() == nil {
		d.setLastErr("会话可能已失效，重开一个会话重试: " + err.Error())
		d.ResetConversation()
		return d.chatStreamOnce(ctx, text, onDelta)
	}
	return ans, err
}

func (d *DoubaoWeb) chatStreamOnce(ctx context.Context, text string, onDelta func(string)) (string, error) {
	d.mu.Lock()
	cookies := make(map[string]string, len(d.sess.Cookies))
	for k, v := range d.sess.Cookies {
		cookies[k] = v
	}
	d.mu.Unlock()

	if cookies["sessionid"] == "" {
		return "", errDoubaoNotLoggedIn
	}

	params := d.securityParams()
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	endpoint := doubaoBaseURL + "/chat/completion?" + q.Encode()

	body, err := json.Marshal(d.completionPayload(text))
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", doubaoUserAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.doubao.com")
	req.Header.Set("Referer", "https://www.doubao.com/chat/")
	if ck := d.cookieHeader(cookies); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	csrf := cookies["passport_csrf_token"]
	if csrf == "" {
		csrf = cookies["passport_csrf_token_default"]
	}
	req.Header.Set("x-tt-passport-csrf-token", csrf)

	resp, err := d.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求豆包失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("豆包返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var e struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Code != 0 {
			if e.Code == 710012001 {
				return "", fmt.Errorf("%w (code=%d %s)", errDoubaoNotLoggedIn, e.Code, e.Msg)
			}
			return "", fmt.Errorf("豆包接口错误 code=%d msg=%s", e.Code, e.Msg)
		}
		return "", fmt.Errorf("豆包返回了非流式响应 (content-type=%s): %.300s", ct, string(raw))
	}

	var sb strings.Builder
	if err := d.consumeSSE(ctx, resp.Body, &sb, onDelta); err != nil {
		return sb.String(), err
	}
	// 一轮问答在会话里新增用户+助手两条消息，下一轮的 last_message_index 要跟上
	if d.currentConvID() != "" {
		d.bumpMessageIndex()
	}
	return sb.String(), nil
}

// consumeSSE 解析豆包 SSE 流。事件类型见 README「SSE 事件类型」。
func (d *DoubaoWeb) consumeSSE(ctx context.Context, r io.Reader, out *strings.Builder, onDelta func(string)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	event := "message"
	var dataLines []string

	flush := func() error {
		if len(dataLines) == 0 {
			event = "message"
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		ev := event
		event = "message"
		return d.handleSSEEvent(ev, payload, out, onDelta)
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		d.rawMu.Lock()
		if d.rawBuf != nil {
			d.rawBuf.WriteString(line)
			d.rawBuf.WriteByte('\n')
		}
		d.rawMu.Unlock()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// 注释/心跳
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			if event == "" {
				event = "message"
			}
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return scanner.Err()
}

func (d *DoubaoWeb) handleSSEEvent(event, payload string, out *strings.Builder, onDelta func(string)) error {
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return nil // 非 JSON 事件直接忽略
	}

	if event == "gateway-error" {
		code := fmt.Sprint(obj["code"])
		msg := fmt.Sprint(obj["message"])
		if code == "710012001" {
			d.setLastErr(msg)
			return fmt.Errorf("%w (gateway-error %s)", errDoubaoNotLoggedIn, msg)
		}
		return fmt.Errorf("豆包网关错误: code=%s message=%s", code, msg)
	}

	// STREAM_MSG_NOTIFY 承载助手消息的首个（或整段）文本，
	// 只处理 user_type=2（助手），user_type=1 是用户自己的消息回显。
	if event == "STREAM_MSG_NOTIFY" {
		if meta, ok := obj["meta"].(map[string]any); ok {
			if ut := toInt(meta["user_type"]); ut == 1 {
				return nil
			}
		}
		if c, ok := obj["content"].(map[string]any); ok {
			if s, ok := c["content"].(string); ok && s != "" {
				var co map[string]any
				if json.Unmarshal([]byte(s), &co) == nil {
					if t, ok := co["text"].(string); ok && t != "" {
						d.emit(t, out, onDelta)
					}
				}
			}
		}
		// 新格式：首个 token 在 content.content_block 里，不能漏（否则每条回复吞掉开头）
		d.emitBlocks(extractContentBlocks(obj), out, onDelta)
		return nil
	}

	if event == "SSE_ACK" || event == "ACK" {
		// 记下会话 id，下一轮复用，避免每句话都在豆包网页里新建一个会话
		if ack, ok := obj["ack_client_meta"].(map[string]any); ok {
			convID := numToString(ack["conversation_id"])
			sectionID := numToString(ack["section_id"])
			if convID != "" && convID != "0" {
				d.setConv(convID, sectionID)
			}
		}
		return nil
	}

	if event == "STREAM_ERROR" || obj["error_code"] != nil {
		code := toInt(obj["error_code"])
		msg := fmt.Sprint(obj["error_msg"])
		if code != 0 {
			if doubaoRiskErrorCodes[code] {
				d.setLastErr(fmt.Sprintf("触发风控 code=%d: %s", code, msg))
			}
			return fmt.Errorf("豆包流错误 code=%d: %s", code, msg)
		}
	}

	// chunk_delta 精简格式：{"text": "..."}
	if t, ok := obj["text"].(string); ok && t != "" {
		if _, hasErr := obj["error_code"]; !hasErr {
			d.emit(t, out, onDelta)
			return nil
		}
	}

	// 完整 content_block 格式
	d.emitBlocks(extractContentBlocks(obj), out, onDelta)

	// patch_op 里的 content 字符串
	if patches, ok := obj["patch_op"].([]any); ok {
		for _, p := range patches {
			pm, _ := p.(map[string]any)
			pv, _ := pm["patch_value"].(map[string]any)
			if pv == nil {
				continue
			}
			if s, ok := pv["content"].(string); ok && s != "" {
				var co map[string]any
				if json.Unmarshal([]byte(s), &co) == nil {
					if t, ok := co["text"].(string); ok && t != "" {
						d.emit(t, out, onDelta)
					}
				}
			}
		}
	}
	return nil
}

// ChatCaptureRaw 发一条消息并返回解析后的回答 + 原始 SSE 文本（调试用）。
func (d *DoubaoWeb) ChatCaptureRaw(ctx context.Context, text string) (string, string, error) {
	buf := &bytes.Buffer{}
	d.rawMu.Lock()
	d.rawBuf = buf
	d.rawMu.Unlock()
	ans, err := d.chatStreamOnce(ctx, text, nil)
	d.rawMu.Lock()
	d.rawBuf = nil
	d.rawMu.Unlock()
	if d.currentConvID() != "" {
		d.bumpMessageIndex()
	}
	return ans, buf.String(), err
}

// emitBlocks 只处理正文文本块（10000），思考块（10040）跳过。
func (d *DoubaoWeb) emitBlocks(blocks []map[string]any, out *strings.Builder, onDelta func(string)) {
	for _, cb := range blocks {
		if toInt(cb["block_type"]) != 10000 {
			continue
		}
		content, _ := cb["content"].(map[string]any)
		if content == nil {
			continue
		}
		tb, _ := content["text_block"].(map[string]any)
		if tb == nil {
			continue
		}
		if t, ok := tb["text"].(string); ok && t != "" {
			d.emit(t, out, onDelta)
		}
	}
}

func (d *DoubaoWeb) emit(t string, out *strings.Builder, onDelta func(string)) {
	out.WriteString(t)
	if onDelta != nil {
		onDelta(t)
	}
}

func (d *DoubaoWeb) setLastErr(msg string) {
	d.mu.Lock()
	d.lastErr = msg
	d.mu.Unlock()
}

func extractContentBlocks(obj map[string]any) []map[string]any {
	var blocks []map[string]any
	collect := func(v any) {
		list, _ := v.([]any)
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				blocks = append(blocks, m)
			}
		}
	}
	if patches, ok := obj["patch_op"].([]any); ok {
		for _, p := range patches {
			pm, _ := p.(map[string]any)
			pv, _ := pm["patch_value"].(map[string]any)
			if pv != nil {
				collect(pv["content_block"])
			}
		}
	}
	if c, ok := obj["content"].(map[string]any); ok {
		collect(c["content_block"])
	}
	return blocks
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
