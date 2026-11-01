package main

// 豆包网页版的会话列表与消息读取（管理台用）。
//
//   POST /im/chain/recent_conv   最近会话（含最后几条消息预览）
//   POST /im/chain/single        某个会话的消息
//
// 这两个接口不需要 msToken（只有部分 /im 接口要），实测可直接调。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type adminConversation struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	LastTime     int64  `json:"last_time"`
	MessageCount int    `json:"message_count"`
}

type adminMessage struct {
	Role string `json:"role"`
	Time int64  `json:"time"`
	Text string `json:"text"`
}

// messageText 从豆包消息的 content 字段（JSON 字符串）里取出纯文本。
func messageText(content string) string {
	if content == "" {
		return ""
	}
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &obj); err == nil && obj.Text != "" {
		return obj.Text
	}
	return ""
}

// ListConversations 读取最近会话。
func (d *DoubaoWeb) ListConversations(ctx context.Context, limit int) ([]adminConversation, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	payload := map[string]any{
		"cmd": 3200,
		"uplink_body": map[string]any{
			"pull_recent_conv_chain_uplink_body": map[string]any{"limit": limit},
		},
		"sequence_id": newUUID(),
		"channel":     2,
		"version":     "1",
	}
	raw, err := d.doPostJSON(ctx, "/im/chain/recent_conv", nil, payload, "application/json; encoding=utf-8")
	if err != nil {
		return nil, err
	}

	var parsed struct {
		StatusCode int    `json:"status_code"`
		StatusDesc string `json:"status_desc"`
		Downlink   struct {
			Pull struct {
				Messages []struct {
					ConversationID string `json:"conversation_id"`
					ConvVersion    string `json:"conv_version"`
					MessageCount   int    `json:"message_count"`
					Messages       []struct {
						Content    string `json:"content"`
						Brief      string `json:"brief"`
						UserType   int    `json:"user_type"`
						CreateTime any    `json:"create_time"`
					} `json:"messages"`
				} `json:"messages"`
			} `json:"pull_recent_conv_chain_downlink_body"`
		} `json:"downlink_body"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析会话列表失败: %w", err)
	}
	if parsed.StatusCode != 0 {
		return nil, fmt.Errorf("读取会话列表失败: code=%d %s", parsed.StatusCode, parsed.StatusDesc)
	}

	out := make([]adminConversation, 0, len(parsed.Downlink.Pull.Messages))
	for _, c := range parsed.Downlink.Pull.Messages {
		if c.ConversationID == "" {
			continue
		}
		conv := adminConversation{ID: c.ConversationID, MessageCount: c.MessageCount}
		// 标题取第一条用户消息，预览取最后一条助手消息
		for _, m := range c.Messages {
			text := messageText(m.Content)
			if text == "" {
				text = m.Brief
			}
			if text == "" {
				continue
			}
			if ts := toInt64(m.CreateTime); ts > conv.LastTime {
				conv.LastTime = ts
			}
			if m.UserType == 1 && conv.Title == "" {
				conv.Title = truncateRunes(text, 24)
			}
			if m.UserType != 1 {
				conv.Preview = truncateRunes(text, 60)
			}
		}
		if conv.Title == "" {
			conv.Title = "（无标题）"
		}
		out = append(out, conv)
	}
	return out, nil
}

// ConversationMessages 读取某个会话的消息。
func (d *DoubaoWeb) ConversationMessages(ctx context.Context, conversationID string, limit int) ([]adminMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, fmt.Errorf("缺少会话 id")
	}

	payload := map[string]any{
		"cmd": 3100,
		"uplink_body": map[string]any{
			"pull_singe_chain_uplink_body": map[string]any{
				"conversation_id":        conversationID,
				"anchor_index":           9007199254740991,
				"conversation_type":      3,
				"direction":              1,
				"limit":                  limit,
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

	var parsed struct {
		StatusCode int    `json:"status_code"`
		StatusDesc string `json:"status_desc"`
		Downlink   struct {
			Pull struct {
				Messages []struct {
					Content    string `json:"content"`
					Brief      string `json:"brief"`
					UserType   int    `json:"user_type"`
					CreateTime any    `json:"create_time"`
				} `json:"messages"`
			} `json:"pull_singe_chain_downlink_body"`
		} `json:"downlink_body"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析消息失败: %w", err)
	}
	if parsed.StatusCode != 0 {
		return nil, fmt.Errorf("读取消息失败: code=%d %s", parsed.StatusCode, parsed.StatusDesc)
	}

	out := make([]adminMessage, 0, len(parsed.Downlink.Pull.Messages))
	for _, m := range parsed.Downlink.Pull.Messages {
		text := messageText(m.Content)
		if text == "" {
			text = m.Brief
		}
		if text == "" {
			continue
		}
		role := "assistant"
		if m.UserType == 1 {
			role = "user"
		}
		out = append(out, adminMessage{Role: role, Time: toInt64(m.CreateTime), Text: text})
	}
	// 接口返回的是从新到旧，反转成正序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// toInt64 兼容接口把时间戳返回成字符串或数字两种情况。
func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		var n int64
		for _, r := range x {
			if r < '0' || r > '9' {
				return 0
			}
			n = n*10 + int64(r-'0')
		}
		return n
	}
	return 0
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

var _ = time.Now
