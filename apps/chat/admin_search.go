package main

// 联网搜索：优先 DeepSeek 的 /v1/tools/search，失败或没 key 时回落豆包。
//
//	POST https://api.deepseek.com/v1/tools/search
//	{"query":"...","count":6}
//	→ results: [{title,url,snippet,summary,date,site_name,rerank_score}]

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type dsSearchResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Date        string  `json:"date"`
	SiteName    string  `json:"site_name"`
	RerankScore float64 `json:"rerank_score"`
}

// deepSeekAPIKey 取 key：config 里优先，其次从 codex_home 的 config.toml 里找。
func (a *adminServer) deepSeekAPIKey() string {
	cfg := a.getConfig()
	if cfg == nil {
		return ""
	}
	if k := strings.TrimSpace(cfg.Search.APIKey); k != "" {
		return k
	}
	home := strings.TrimSpace(cfg.Codex.CodexHome)
	if home == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`experimental_bearer_token\s*=\s*"([^"]+)"`)
	if m := re.FindStringSubmatch(string(raw)); len(m) == 2 {
		return m[1]
	}
	return ""
}

func (a *adminServer) deepSeekSearch(ctx context.Context, query string) ([]dsSearchResult, error) {
	key := a.deepSeekAPIKey()
	if key == "" {
		return nil, fmt.Errorf("没有 DeepSeek key")
	}
	count := 6
	if cfg := a.getConfig(); cfg != nil && cfg.Search.Count > 0 {
		count = cfg.Search.Count
	}
	body, _ := json.Marshal(map[string]any{"query": query, "count": count})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.deepseek.com/v1/tools/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("搜索请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("搜索失败 HTTP %d: %s", resp.StatusCode, shortenText(string(raw), 160))
	}
	var parsed struct {
		Results []dsSearchResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析搜索结果失败: %w", err)
	}
	return parsed.Results, nil
}

// formatSearchResults 把结果整理成模型好读的紧凑文本。
func formatSearchResults(results []dsSearchResult, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range results {
		if n >= max {
			break
		}
		text := strings.TrimSpace(r.Summary)
		if text == "" {
			text = strings.TrimSpace(r.Snippet)
		}
		if text == "" && r.Title == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d. %s", n, r.Title)
		if r.SiteName != "" {
			fmt.Fprintf(&b, "（%s", r.SiteName)
			if r.Date != "" {
				fmt.Fprintf(&b, " · %s", r.Date)
			}
			b.WriteString("）")
		}
		b.WriteString("\n")
		if text != "" {
			fmt.Fprintf(&b, "   %s\n", shortenText(text, 220))
		}
		if r.URL != "" {
			fmt.Fprintf(&b, "   %s\n", r.URL)
		}
	}
	return strings.TrimSpace(b.String())
}
