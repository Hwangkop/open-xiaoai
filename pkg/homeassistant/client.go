package homeassistant

// Home Assistant REST API 客户端（长期访问令牌）。
//
// 只用两个接口：
//   GET  /api/states                  全部实体状态
//   POST /api/services/{domain}/{svc} 调用服务（开灯、调温……）

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.BaseURL != "" && c.Token != ""
}

// State 是 /api/states 里的一条。
type State struct {
	EntityID   string         `json:"entity_id"`
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes"`
}

// FriendlyName 实体显示名，没有就退回 entity_id。
func (s State) FriendlyName() string {
	if v, ok := s.Attributes["friendly_name"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return s.EntityID
}

// Domain 实体域，如 light / switch。
func (s State) Domain() string {
	if i := strings.IndexByte(s.EntityID, '.'); i > 0 {
		return s.EntityID[:i]
	}
	return ""
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("连接 Home Assistant 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("Home Assistant 令牌无效或已失效")
	case resp.StatusCode >= 300:
		return fmt.Errorf("Home Assistant 返回 %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析 Home Assistant 响应失败: %w", err)
		}
	}
	return nil
}

// Ping 检查地址和令牌是否可用。
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/", nil, nil)
}

// States 拉取全部实体状态。
func (c *Client) States(ctx context.Context) ([]State, error) {
	var out []State
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CallService 调用一个服务，data 里通常带 entity_id。
func (c *Client) CallService(ctx context.Context, domain, service string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	return c.do(ctx, http.MethodPost, "/api/services/"+domain+"/"+service, data, nil)
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
