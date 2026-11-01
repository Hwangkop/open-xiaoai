package netease

// 网易云扫码登录。
//
//  1. POST /api/login/qrcode/unikey {type:3}     → unikey
//  2. 二维码内容：https://music.163.com/login?codekey=<unikey>
//  3. POST /api/login/qrcode/client/login {key,type:3} 轮询
//     code: 800 过期 / 801 等待扫码 / 802 已扫待确认 / 803 成功（Set-Cookie 里有 MUSIC_U）

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// QRLogin 一次扫码登录会话。
type QRLogin struct {
	Key   string
	Image []byte // PNG
}

// CreateQR 申请一个登录二维码。
func (c *Client) CreateQR(ctx context.Context) (*QRLogin, error) {
	body, err := c.call(ctx, "/api/login/qrcode/unikey", map[string]any{"type": 3})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Code   int    `json:"code"`
		Unikey string `json:"unikey"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析二维码响应失败: %w", err)
	}
	if parsed.Unikey == "" {
		return nil, fmt.Errorf("网易云没有返回二维码 key（code=%d）", parsed.Code)
	}

	content := "https://music.163.com/login?codekey=" + parsed.Unikey
	png, err := qrcode.Encode(content, qrcode.Medium, 320)
	if err != nil {
		return nil, fmt.Errorf("生成二维码图片失败: %w", err)
	}
	return &QRLogin{Key: parsed.Unikey, Image: png}, nil
}

// QRStatus 扫码状态。
type QRStatus struct {
	Code   int    // 800/801/802/803
	State  string // expired / waiting / scanned / confirmed
	Cookie string // 仅在 confirmed 时有值
}

// CheckQR 轮询扫码状态。
func (c *Client) CheckQR(ctx context.Context, key string) (*QRStatus, error) {
	resp, err := c.callRaw(ctx, "/api/login/qrcode/client/login", map[string]any{
		"key": key, "type": 3,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Cookie  string `json:"cookie"` // 部分版本直接放在 body 里
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("解析扫码状态失败: %w", err)
	}

	st := &QRStatus{Code: parsed.Code}
	switch parsed.Code {
	case 800:
		st.State = "expired"
	case 801:
		st.State = "waiting"
	case 802:
		st.State = "scanned"
	case 803:
		st.State = "confirmed"
		st.Cookie = mergeSetCookies(resp, parsed.Cookie)
		if cookieValue(st.Cookie, "MUSIC_U") == "" {
			return nil, fmt.Errorf("登录成功但没拿到 MUSIC_U cookie")
		}
	default:
		st.State = "waiting"
	}
	return st, nil
}

// mergeSetCookies 把 Set-Cookie 与 body 里的 cookie 合并成 "k=v; k=v"。
func mergeSetCookies(resp *http.Response, bodyCookie string) string {
	kv := map[string]string{}
	add := func(pair string) {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			return
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return
		}
		kv[k] = v
	}
	for _, raw := range resp.Header.Values("Set-Cookie") {
		if i := strings.Index(raw, ";"); i >= 0 {
			raw = raw[:i]
		}
		add(raw)
	}
	for _, part := range strings.Split(bodyCookie, ";") {
		add(part)
	}
	parts := make([]string, 0, len(kv))
	for k, v := range kv {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// Account 当前登录账号信息（昵称、VIP 类型）。未登录时 LoggedIn=false。
type Account struct {
	LoggedIn bool   `json:"logged_in"`
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname"`
	VIPType  int    `json:"vip_type"`
}

// GetAccount 查询登录态。cookie 失效时返回 LoggedIn=false。
func (c *Client) GetAccount(ctx context.Context) (*Account, error) {
	if cookieValue(c.Cookie, "MUSIC_U") == "" {
		return &Account{}, nil
	}
	body, err := c.call(ctx, "/api/w/nuser/account/get", map[string]any{})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Code    int `json:"code"`
		Profile struct {
			UserID   int64  `json:"userId"`
			Nickname string `json:"nickname"`
			VIPType  int    `json:"vipType"`
		} `json:"profile"`
		Account struct {
			ID int64 `json:"id"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析账号信息失败: %w", err)
	}
	if parsed.Profile.UserID == 0 && parsed.Account.ID == 0 {
		return &Account{}, nil
	}
	id := parsed.Profile.UserID
	if id == 0 {
		id = parsed.Account.ID
	}
	return &Account{
		LoggedIn: true,
		UserID:   fmt.Sprintf("%d", id),
		Nickname: parsed.Profile.Nickname,
		VIPType:  parsed.Profile.VIPType,
	}, nil
}
