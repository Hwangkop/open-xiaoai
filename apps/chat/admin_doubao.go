package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const doubaoProxyTimeout = 30 * time.Second

// doubaoConfig 读取当前 doubao2api 配置（跟随配置热更新）。
func (a *adminServer) doubaoConfig() Doubao2APIConfig {
	if a.getDoubaoConfig == nil {
		return Doubao2APIConfig{}
	}
	return a.getDoubaoConfig()
}

// doubaoCall 以 Bearer 鉴权访问 doubao2api，返回状态码与原始响应体。
func (a *adminServer) doubaoCall(ctx context.Context, method, path string) (int, []byte, error) {
	cfg := a.doubaoConfig()
	if !cfg.Enabled() {
		return 0, nil, fmt.Errorf("未配置 doubao2api.base_url")
	}
	ctx, cancel := context.WithTimeout(ctx, doubaoProxyTimeout)
	defer cancel()

	url := strings.TrimRight(cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.APIKey))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("无法连接 doubao2api（%s）：%v", cfg.BaseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// handleDoubaoHealth 返回 doubao2api 的登录状态，并附带 base_url 方便前端展示。
func (a *adminServer) handleDoubaoHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	// 优先用原生直连（doubao_web）：登录态、二维码都由我们自己处理
	if web := a.doubaoWebClient(); web != nil {
		st := web.Status()
		st["enabled"] = true
		st["reachable"] = true
		st["mode"] = "native"
		if logged, _ := st["logged_in"].(bool); !logged {
			st["message"] = "未登录，点右侧二维码扫码"
		}
		writeAdminJSON(w, http.StatusOK, st)
		return
	}

	cfg := a.doubaoConfig()
	if !cfg.Enabled() {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "未启用豆包登录（配置 doubao_web.enabled）",
		})
		return
	}

	result := map[string]any{"enabled": true, "base_url": cfg.BaseURL}
	status, body, err := a.doubaoCall(r.Context(), http.MethodGet, "/health")
	if err != nil {
		result["reachable"] = false
		result["error"] = err.Error()
		writeAdminJSON(w, http.StatusOK, result)
		return
	}
	result["reachable"] = true
	result["upstream_status"] = status

	var upstream map[string]any
	if err := json.Unmarshal(body, &upstream); err == nil {
		for k, v := range upstream {
			result[k] = v
		}
	}
	writeAdminJSON(w, http.StatusOK, result)
}

// handleDoubaoLogin 代理 doubao2api 的扫码登录：
//
//	POST /v1/session/qr-login  生成二维码
//	GET  /v1/session/qr-login  轮询登录状态
func (a *adminServer) handleDoubaoLogin(w http.ResponseWriter, r *http.Request) {
	// 原生直连模式：POST 取二维码，GET 轮询
	if web := a.doubaoWebClient(); web != nil {
		if r.Method == http.MethodPost {
			a.handleDoubaoWebQRStart(w, r)
			return
		}
		a.handleDoubaoWebQRPoll(w, r)
		return
	}

	cfg := a.doubaoConfig()
	if !cfg.Enabled() {
		writeAdminError(w, http.StatusServiceUnavailable, "未启用豆包登录")
		return
	}

	var method string
	switch r.Method {
	case http.MethodPost:
		method = http.MethodPost
	case http.MethodGet:
		method = http.MethodGet
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	status, body, err := a.doubaoCall(r.Context(), method, "/v1/session/qr-login")
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// --- 会话记录 ---
//
// 豆包的会话接口（/im/*）需要 Cookie 和网关签名，Go 侧无法直接调用。
// 这里借用 doubao2api 已经启动的浏览器：通过它的 /auth/eval 在页面里发起请求，
// 页面自身会带上 Cookie 和签名。

const (
	maxConversationLimit = 50
	maxMessageLimit      = 100
)

var digitsOnly = regexp.MustCompile(`^[0-9]{1,32}$`)

// doubaoEval 在 doubao2api 的浏览器页面里执行 JS，并返回结果。
func (a *adminServer) doubaoEval(ctx context.Context, js string) (json.RawMessage, error) {
	cfg := a.doubaoConfig()
	if !cfg.Enabled() {
		return nil, fmt.Errorf("未配置 doubao2api.base_url")
	}
	ctx, cancel := context.WithTimeout(ctx, doubaoProxyTimeout)
	defer cancel()

	payload, _ := json.Marshal(map[string]string{"js": js})
	url := strings.TrimRight(cfg.BaseURL, "/") + "/auth/eval"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.APIKey))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 doubao2api（%s）：%v", cfg.BaseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doubao2api 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var wrapper struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("解析 eval 结果失败: %w", err)
	}
	if wrapper.Error != "" {
		return nil, fmt.Errorf("页面执行失败: %s", wrapper.Error)
	}
	if len(wrapper.Result) == 0 {
		return nil, fmt.Errorf("页面没有返回结果（doubao2api 的浏览器可能还没就绪）")
	}
	return wrapper.Result, nil
}

// evalCommonPrelude 是会话接口共用的准备工作：从页面已发出的请求里取公共查询参数。
const evalCommonPrelude = `
  const qs = (performance.getEntriesByType('resource').map(e => e.name)
    .find(n => n.includes('/im/chain/single')) || '').split('?')[1] || '';
  const H = {'Content-Type': 'application/json; encoding=utf-8', 'Agw-Js-Conv': 'str', 'Accept': 'application/json, text/plain, */*'};
  const post = async (path, cmd, key, uplink) => {
    const body = JSON.stringify({cmd, uplink_body: {[key]: uplink},
      sequence_id: (crypto.randomUUID ? crypto.randomUUID() : String(Date.now())), channel: 2, version: '1'});
    const r = await fetch(path + '?' + qs, {method: 'POST', credentials: 'include', headers: H, body});
    return await r.json();
  };
`

func (a *adminServer) handleDoubaoConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	limit := clampInt(r.URL.Query().Get("limit"), 20, 1, maxConversationLimit)

	if web := a.doubaoWebClient(); web != nil {
		list, err := web.ListConversations(r.Context(), limit)
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, list)
		return
	}

	result, err := a.doubaoEval(r.Context(), doubaoConversationsJS(limit))
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(result)
}

// doubaoConversationsJS 生成"最近会话列表"的页面脚本。
// 用 /im/chain/recent_conv 拿到会话分组，再用 /im/conversation/batch_get 补会话名。
func doubaoConversationsJS(limit int) string {
	return `(async () => {` + evalCommonPrelude + fmt.Sprintf(`
    const recent = await post('/im/chain/recent_conv', 3200, 'pull_recent_conv_chain_uplink_body', {limit: %d});
    const groups = (((recent.downlink_body || {}).pull_recent_conv_chain_downlink_body || {}).messages) || [];
    const items = [];
    for (const g of groups) {
      const ms = g.messages || [];
      const last = ms[ms.length - 1] || {};
      const text = ((last.content_block || []).filter(b => b.block_type === 10000)
        .map(b => ((b.content || {}).text_block || {}).text || '').join(' ')).replace(/\s+/g, ' ').slice(0, 80);
      items.push({ id: String(g.conversation_id || ''), last_time: Number(last.create_time || 0),
                   preview: text, message_count: ms.length, title: '' });
      if (items.length >= %d) break;
    }
    const ids = items.map(x => x.id).filter(Boolean);
    if (ids.length) {
      try {
        const info = await post('/im/conversation/batch_get', 1111, 'batch_get_conv_info_uplink_body',
          {conversation_id: ids, option: {recent_message_count_per_conv: 1}, ext: {}});
        const list = (((info.downlink_body || {}).batch_get_conv_info_downlink_body || {}).conversation_info_list) || [];
        const byId = {};
        for (const c of list) byId[String(c.conversation_id)] = c.name || '';
        for (const it of items) it.title = byId[it.id] || '';
      } catch (e) {}
    }
    return items.filter(x => x.id);
  })()`, limit, limit)
}

func (a *adminServer) handleDoubaoMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	conversationID := strings.TrimSpace(r.URL.Query().Get("id"))
	if web := a.doubaoWebClient(); web != nil {
		if conversationID == "" {
			writeAdminError(w, http.StatusBadRequest, "缺少 id 参数")
			return
		}
		list, err := web.ConversationMessages(r.Context(), conversationID, clampInt(r.URL.Query().Get("limit"), 50, 1, 200))
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, list)
		return
	}
	if !digitsOnly.MatchString(conversationID) {
		writeAdminError(w, http.StatusBadRequest, "conversation id 不合法")
		return
	}
	limit := clampInt(r.URL.Query().Get("limit"), 20, 1, maxMessageLimit)

	result, err := a.doubaoEval(r.Context(), doubaoMessagesJS(conversationID, limit))
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(result)
}

// doubaoMessagesJS 生成"拉取某个会话消息"的页面脚本。
// conversationID 必须已通过纯数字校验，才能安全内联。
func doubaoMessagesJS(conversationID string, limit int) string {
	return `(async () => {` + evalCommonPrelude + fmt.Sprintf(`
    const chain = await post('/im/chain/single', 3100, 'pull_singe_chain_uplink_body', {
      conversation_id: '%s', anchor_index: 9007199254740991, conversation_type: 3,
      direction: 1, limit: %d, ext: {}, filter: {index_list: []},
      evaluate_ab_params: '', evaluate_common_params: '', option: {lazy_load_strategy: 0}
    });
    const list = (((chain.downlink_body || {}).pull_singe_chain_downlink_body || {}).messages) || [];
    const out = [];
    for (const m of list) {
      const text = ((m.content_block || []).filter(b => b.block_type === 10000)
        .map(b => ((b.content || {}).text_block || {}).text || '').join('\n')).trim();
      if (!text) continue;
      out.push({ role: m.user_type === 1 ? 'user' : 'assistant',
                 time: Number(m.create_time || 0), text: text });
    }
    out.sort((a, b) => a.time - b.time);
    return out.slice(-%d);
  })()`, conversationID, limit, limit)
}

func clampInt(raw string, fallback, min, max int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// handleDoubaoJobs 返回定时任务列表与轮询状态（只读）。
func (a *adminServer) handleDoubaoJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	watcher := a.getScheduleValue()
	if watcher == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "定时任务播报未启用（config 里 schedules.enabled 为 false）",
		})
		return
	}
	jobs, err := watcher.JobsSnapshot(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	lastPoll, lastErr := watcher.Status()
	resp := map[string]any{
		"enabled":  true,
		"jobs":     jobs,
		"last_err": lastErr,
		"interval": watcher.Interval().String(),
	}
	if !lastPoll.IsZero() {
		resp["last_poll"] = lastPoll.Format("15:04:05")
	}
	writeAdminJSON(w, http.StatusOK, resp)
}

type doubaoSpeakRequest struct {
	JobID string `json:"job_id"`
	Text  string `json:"text"`
}

// handleDoubaoSpeak 调试接口：立即播报某个任务的最新结果，或直接播报一段文本。
func (a *adminServer) handleDoubaoSpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, adminMaxRequestBytes)
	var req doubaoSpeakRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}

	watcher := a.getScheduleValue()
	if watcher == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "定时任务播报未启用")
		return
	}

	// 直接给了文本：清洗后播报
	if strings.TrimSpace(req.Text) != "" {
		text := CleanForSpeech(req.Text)
		if text == "" {
			writeAdminError(w, http.StatusBadRequest, "清洗后没有可播报内容")
			return
		}
		if !watcher.Enqueue(text) {
			writeAdminError(w, http.StatusServiceUnavailable, "播报队列已满")
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"message": "已加入播报队列", "text": text})
		return
	}

	if !digitsOnly.MatchString(strings.TrimSpace(req.JobID)) {
		writeAdminError(w, http.StatusBadRequest, "job_id 不合法")
		return
	}
	text, err := watcher.SpeakLatest(r.Context(), strings.TrimSpace(req.JobID))
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"message": "已加入播报队列", "text": text})
}

// handleDoubaoJobsPoll 立即触发一轮轮询（调试用）。
func (a *adminServer) handleDoubaoJobsPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	watcher := a.getScheduleValue()
	if watcher == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "定时任务播报未启用")
		return
	}
	watcher.PollNow(r.Context())
	_, lastErr := watcher.Status()
	writeAdminJSON(w, http.StatusOK, map[string]any{"message": "已执行一轮轮询", "last_err": lastErr})
}

func (a *adminServer) getScheduleValue() *ScheduleWatcher {
	if a.getSchedule == nil {
		return nil
	}
	return a.getSchedule()
}
