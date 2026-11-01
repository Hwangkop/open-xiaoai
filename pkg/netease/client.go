package netease

// 网易云音乐客户端：搜索歌曲 + 取播放地址。
//
// 只需要登录后的 MUSIC_U cookie（免费歌可以不登录）。
// 播放地址是临时的（约 90 秒内有效），拿到后要尽快交给播放器。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

type Client struct {
	Cookie string // 形如 "MUSIC_U=xxx; __csrf=yyy"
	HTTP   *http.Client
}

func New(cookie string) *Client {
	return &Client{Cookie: cookie, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type Song struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Artists  string `json:"artists"`
	Album    string `json:"album"`
	Duration int    `json:"duration_ms"`
}

// Search 搜索单曲。
func (c *Client) Search(ctx context.Context, keyword string, limit int) ([]Song, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	body, err := c.call(ctx, "/api/search/get", map[string]any{
		"s": keyword, "type": 1, "limit": limit, "offset": 0,
	})
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Code   int `json:"code"`
		Result struct {
			Songs []struct {
				ID       int64  `json:"id"`
				Name     string `json:"name"`
				Duration int    `json:"duration"`
				Artists  []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Album struct {
					Name string `json:"name"`
				} `json:"album"`
			} `json:"songs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析搜索结果失败: %w", err)
	}
	if parsed.Code != 200 {
		return nil, fmt.Errorf("搜索失败 code=%d", parsed.Code)
	}

	out := make([]Song, 0, len(parsed.Result.Songs))
	for _, s := range parsed.Result.Songs {
		names := make([]string, 0, len(s.Artists))
		for _, a := range s.Artists {
			names = append(names, a.Name)
		}
		out = append(out, Song{
			ID: s.ID, Name: s.Name, Artists: strings.Join(names, "/"),
			Album: s.Album.Name, Duration: s.Duration,
		})
	}
	return out, nil
}

// SongURL 取播放地址。level 可选 standard/higher/exhigh/lossless。
// 先试 v1 接口（支持音质等级），失败再退回 br 接口。
func (c *Client) SongURL(ctx context.Context, id int64, level string) (string, error) {
	if level == "" {
		level = "standard"
	}
	ids := "[" + strconv.FormatInt(id, 10) + "]"

	body, err := c.call(ctx, "/api/song/enhance/player/url/v1", map[string]any{
		"ids": ids, "level": level, "encodeType": "flac",
	})
	if err == nil {
		if u := parseURL(body); u != "" {
			return u, nil
		}
	}

	body, err = c.call(ctx, "/api/song/enhance/player/url", map[string]any{
		"ids": ids, "br": 128000,
	})
	if err != nil {
		return "", err
	}
	if u := parseURL(body); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("没有拿到播放地址（可能需要会员或该曲目无版权）")
}

func parseURL(body []byte) string {
	var parsed struct {
		Code int `json:"code"`
		Data []struct {
			URL   string `json:"url"`
			Type  string `json:"type"`
			Br    int    `json:"br"`
			Trial bool   `json:"freeTrialInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	// eapi 的响应往往不带 code 字段，只要 data[0].url 有值就说明成功
	if len(parsed.Data) == 0 {
		return ""
	}
	if parsed.Code != 0 && parsed.Code != 200 {
		return ""
	}
	return parsed.Data[0].URL
}

// call 发一个 eapi 加密请求（网易云桌面客户端接口）。
// 相比 weapi，eapi 需要额外的 header 块，且 cookie 要按 header 逐项拼。
func (c *Client) call(ctx context.Context, path string, data map[string]any) ([]byte, error) {
	resp, err := c.callRaw(ctx, path, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if len(raw) == 0 {
		return nil, fmt.Errorf("HTTP 200 但响应为空")
	}
	return raw, nil
}

// callRaw 同 call，但返回原始响应（需要读 Set-Cookie 时用，比如扫码登录）。
func (c *Client) callRaw(ctx context.Context, path string, data map[string]any) (*http.Response, error) {
	csrf := csrfFromCookie(c.Cookie)
	header := map[string]any{
		"osver":       "Microsoft-Windows-10-Professional-build-19045-64bit",
		"os":          "pc",
		"appver":      "3.1.17.204416",
		"versioncode": "140",
		"channel":     "netease",
		"requestId":   fmt.Sprintf("%d_%04d", time.Now().UnixMilli(), time.Now().Nanosecond()%1000),
		"__csrf":      csrf,
	}
	if v := cookieValue(c.Cookie, "MUSIC_U"); v != "" {
		header["MUSIC_U"] = v
	}
	if v := cookieValue(c.Cookie, "MUSIC_A"); v != "" {
		header["MUSIC_A"] = v
	}

	body := map[string]any{}
	for k, v := range data {
		body[k] = v
	}
	body["e_r"] = false
	body["header"] = header

	form, err := eapiForm(path, body)
	if err != nil {
		return nil, err
	}

	u := eapiDomain + "/eapi/" + strings.TrimPrefix(path, "/api/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", webDomain)

	// cookie 按 header 逐项拼接（网易云要求）
	parts := make([]string, 0, len(header))
	for k, v := range header {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(fmt.Sprint(v)))
	}
	req.Header.Set("Cookie", strings.Join(parts, "; "))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d（内容 %.80s）", resp.StatusCode, string(raw))
	}
	return resp, nil
}

func cookieValue(cookie, name string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, name+"="); ok {
			return v
		}
	}
	return ""
}

func csrfFromCookie(cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "__csrf="); ok {
			return v
		}
	}
	return ""
}

// 确保 url 包被用到（weapiForm 返回 url.Values）
var _ = url.Values{}
