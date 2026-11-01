package main

// 豆包网页版的定时任务读取（只读）。
//
// 原来这部分靠 doubao2api 的 /auth/eval 在浏览器里跑 JS，现在用原生 cookie 直接调：
//   POST /alice/job_cron/list  → 任务列表
//   POST /im/chain/single      → 某个会话的最新消息（任务执行结果）
//
// 任务本身由用户在豆包网页/App 里创建，这里只读，不创建、不修改。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// doPostJSON 带登录态发 JSON POST 到豆包接口，返回原始响应体。
func (d *DoubaoWeb) doPostJSON(ctx context.Context, path string, query url.Values, payload any, contentType string) ([]byte, error) {
	d.mu.Lock()
	cookies := make(map[string]string, len(d.sess.Cookies))
	for k, v := range d.sess.Cookies {
		cookies[k] = v
	}
	d.mu.Unlock()

	if cookies["sessionid"] == "" {
		return nil, errDoubaoNotLoggedIn
	}

	if query == nil {
		query = url.Values{}
	}
	if query.Get("aid") == "" {
		for k, v := range d.securityParams() {
			query.Set(k, v)
		}
	}
	endpoint := doubaoBaseURL + path + "?" + query.Encode()

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("User-Agent", doubaoUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Origin", "https://www.doubao.com")
	req.Header.Set("Referer", "https://www.doubao.com/chat/")
	req.Header.Set("Agw-Js-Conv", "str")
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
		return nil, fmt.Errorf("请求 %s 失败: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 %d: %.200s", path, resp.StatusCode, string(raw))
	}
	return raw, nil
}

// numToString 把 JSON 里的 string/number 统一成字符串。
// last_run_time 在接口里可能是数字时间戳，也可能是字符串。
func numToString(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return n
	case json.Number:
		return n.String()
	case float64:
		return fmt.Sprintf("%.0f", n)
	default:
		return fmt.Sprint(n)
	}
}

// ListJobs 读取豆包定时任务列表。任务由用户在豆包界面创建，这里只读。
func (d *DoubaoWeb) ListJobs(ctx context.Context) ([]doubaoJob, error) {
	raw, err := d.doPostJSON(ctx, "/alice/job_cron/list", nil,
		map[string]any{"size": 50, "sort_order": 1}, "application/json")
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Jobs []struct {
				JobID          string `json:"job_id"`
				JobType        int    `json:"job_type"`
				ConversationID string `json:"conversation_id"`
				Content        struct {
					CronJob struct {
						Title         string `json:"title"`
						Status        int    `json:"status"`
						LastRunTime   any    `json:"last_run_time"`
						LastRunStatus int    `json:"last_run_status"`
					} `json:"cron_job"`
				} `json:"content"`
			} `json:"jobs"`
		} `json:"data"`
	}
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("解析任务列表失败: %w", err)
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("读取任务列表失败: code=%d msg=%s", parsed.Code, parsed.Msg)
	}

	jobs := make([]doubaoJob, 0, len(parsed.Data.Jobs))
	for _, j := range parsed.Data.Jobs {
		cj := j.Content.CronJob
		jobs = append(jobs, doubaoJob{
			ID:             j.JobID,
			Type:           j.JobType,
			Title:          cj.Title,
			Status:         cj.Status,
			LastRunTime:    numToString(cj.LastRunTime),
			LastRunStatus:  cj.LastRunStatus,
			ConversationID: j.ConversationID,
		})
	}
	return jobs, nil
}

// LatestAssistantMessage 读取某个会话里最新的一条助手回复，用于播报定时任务结果。
func (d *DoubaoWeb) LatestAssistantMessage(ctx context.Context, conversationID string) (*messageResult, error) {
	payload := map[string]any{
		"cmd": 3100,
		"uplink_body": map[string]any{
			"pull_singe_chain_uplink_body": map[string]any{
				"conversation_id":        conversationID,
				"anchor_index":           9007199254740991,
				"conversation_type":      3,
				"direction":              1,
				"limit":                  5,
				"ext":                    map[string]any{},
				"filter":                 map[string]any{"index_list": []any{}},
				"evaluate_ab_params":     "",
				"evaluate_common_params": "",
				"option":                 map[string]any{"lazy_load_strategy": 0},
			},
		},
		"sequence_id": newUUID(),
		"channel":     2,
		"version":     "1",
	}

	raw, err := d.doPostJSON(ctx, "/im/chain/single", nil, payload, "application/json; encoding=utf-8")
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed struct {
		DownlinkBody struct {
			Pull struct {
				Messages []struct {
					MessageID    string `json:"message_id"`
					UserType     int    `json:"user_type"`
					CreateTime   any    `json:"create_time"`
					ContentBlock []struct {
						BlockType int `json:"block_type"`
						Content   struct {
							TextBlock struct {
								Text string `json:"text"`
							} `json:"text_block"`
						} `json:"content"`
					} `json:"content_block"`
				} `json:"messages"`
			} `json:"pull_singe_chain_downlink_body"`
		} `json:"downlink_body"`
	}
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("解析会话消息失败: %w", err)
	}

	var best *messageResult
	var bestTime int64 = -1
	for _, m := range parsed.DownlinkBody.Pull.Messages {
		if m.UserType == 1 { // 用户自己发的，跳过
			continue
		}
		var parts []string
		for _, b := range m.ContentBlock {
			if b.BlockType == 10000 && b.Content.TextBlock.Text != "" {
				parts = append(parts, b.Content.TextBlock.Text)
			}
		}
		text := strings.TrimSpace(strings.Join(parts, "\n"))
		if text == "" {
			continue
		}
		var ts int64
		switch v := m.CreateTime.(type) {
		case json.Number:
			ts, _ = v.Int64()
		case float64:
			ts = int64(v)
		}
		if ts >= bestTime {
			bestTime = ts
			best = &messageResult{ID: m.MessageID, Text: text}
		}
	}
	if best == nil {
		return nil, nil
	}
	return best, nil
}

var _ = time.Now
