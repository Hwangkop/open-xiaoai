package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const bilibiliUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

var bilibiliMixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40,
	61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11,
	36, 20, 34, 44, 52,
}

type bilibiliService struct {
	mu           sync.Mutex
	bootstrapMu  sync.Mutex
	bootstrapped bool
	wbiMu        sync.Mutex
	wbiImgKey    string
	wbiSubKey    string
	wbiFetchedAt time.Time
	dir          string
	musicDir     string
	ffmpeg       string
	cookie       string
	store        *bilibiliStore
	maxBytes     int64
	searchLimit  int
	client       *http.Client
	streamClient *http.Client
	searchMu     sync.Mutex
	searchCache  map[string]bilibiliSearchCache
}

type bilibiliSearchCache struct {
	Items   []bilibiliSearchItem
	SavedAt time.Time
}

type bilibiliSession struct {
	Cookie   string `json:"cookie"`
	Nickname string `json:"nickname,omitempty"`
	SavedAt  int64  `json:"saved_at"`
}

type bilibiliStore struct {
	mu   sync.RWMutex
	path string
	sess bilibiliSession
}

type bilibiliSearchItem struct {
	BVID            string `json:"bvid"`
	AID             int64  `json:"aid"`
	Title           string `json:"title"`
	Author          string `json:"author"`
	Duration        string `json:"duration"`
	DurationSeconds int    `json:"duration_seconds"`
	Cover           string `json:"cover"`
	URL             string `json:"url"`
	Score           int    `json:"score"`
	Recommended     bool   `json:"recommended"`
}

type bilibiliVideo struct {
	BVID           string
	AID            int64
	CID            int64
	Title          string
	Author         string
	PageName       string
	Duration       int
	Cover          string
	AudioURL       string
	AudioBackups   []string
	AudioID        int
	AudioBandwidth int
	AudioCodec     string
}

type bilibiliCachedAudio struct {
	Video      bilibiliVideo
	FileName   string
	Path       string
	SourcePath string
}

type bilibiliAudioTrack struct {
	ID        int      `json:"id"`
	BaseURL   string   `json:"baseUrl"`
	BackupURL []string `json:"backupUrl"`
	MimeType  string   `json:"mimeType"`
	Codecs    string   `json:"codecs"`
	Bandwidth int      `json:"bandwidth"`
	Size      int64    `json:"size"`
}

type bilibiliAudioTracks []bilibiliAudioTrack

func (tracks *bilibiliAudioTracks) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*tracks = nil
		return nil
	}
	if data[0] == '[' {
		var list []bilibiliAudioTrack
		if err := json.Unmarshal(data, &list); err != nil {
			return err
		}
		*tracks = list
		return nil
	}
	var one bilibiliAudioTrack
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*tracks = []bilibiliAudioTrack{one}
	return nil
}

type bilibiliQR struct {
	Key      string `json:"key"`
	URL      string `json:"url"`
	State    string `json:"state"`
	Code     int    `json:"code"`
	Nickname string `json:"nickname,omitempty"`
}

func newBilibiliService(configPath string, cfg BilibiliConfig) (*bilibiliService, error) {
	dir := strings.TrimSpace(cfg.CacheDir)
	if dir == "" {
		dir = filepath.Join(filepath.Dir(configPath), "bilibili")
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(configPath), dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析 B 站缓存目录失败: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 B 站缓存目录失败: %w", err)
	}
	musicDir := strings.TrimSpace(cfg.MusicDir)
	if musicDir == "" {
		musicDir = filepath.Join(filepath.Dir(configPath), "music")
	} else if !filepath.IsAbs(musicDir) {
		musicDir = filepath.Join(filepath.Dir(configPath), musicDir)
	}
	musicDir, err = filepath.Abs(musicDir)
	if err != nil {
		return nil, fmt.Errorf("解析 B 站音乐目录失败: %w", err)
	}
	if err := os.MkdirAll(musicDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 B 站音乐目录失败: %w", err)
	}
	ffmpeg := strings.TrimSpace(cfg.Ffmpeg)
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	maxBytes := int64(cfg.MaxSizeMB) << 20
	if maxBytes <= 0 {
		maxBytes = 800 << 20
	}
	limit := cfg.SearchLimit
	if limit <= 0 {
		limit = 12
	}
	if limit > 20 {
		limit = 20
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &bilibiliService{
		dir:          dir,
		musicDir:     musicDir,
		ffmpeg:       ffmpeg,
		cookie:       strings.TrimSpace(cfg.Cookie),
		store:        newBilibiliStore(configPath),
		maxBytes:     maxBytes,
		searchLimit:  limit,
		client:       &http.Client{Timeout: 30 * time.Second, Jar: jar},
		streamClient: &http.Client{},
		searchCache:  map[string]bilibiliSearchCache{},
	}, nil
}

func newBilibiliStore(configPath string) *bilibiliStore {
	path := filepath.Join(filepath.Dir(configPath), "bilibili-session.json")
	store := &bilibiliStore{path: path}
	if raw, err := os.ReadFile(path); err == nil {
		var session bilibiliSession
		if json.Unmarshal(raw, &session) == nil {
			store.sess = session
		}
	}
	return store
}

func (s *bilibiliStore) cookie() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.sess.Cookie)
}

func (s *bilibiliStore) savedAt() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sess.SavedAt
}

func (s *bilibiliStore) nickname() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sess.Nickname
}

func (s *bilibiliStore) save(cookie, nickname string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess = bilibiliSession{
		Cookie:   strings.TrimSpace(cookie),
		Nickname: strings.TrimSpace(nickname),
		SavedAt:  time.Now().Unix(),
	}
	raw, err := json.MarshalIndent(s.sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}

func (s *bilibiliService) cookieHeader() string {
	if cookie := s.store.cookie(); cookie != "" {
		return cookie
	}
	return s.cookie
}

// requestCookie 合并账号 Cookie 与匿名设备 Cookie。
// 直接写死一个 Cookie 头会让 net/http 跳过 CookieJar，登录后 buvid 反而丢失。
func (s *bilibiliService) requestCookie(rawURL string) string {
	values := map[string]string{}
	if parsed, err := url.Parse(rawURL); err == nil && s.client != nil && s.client.Jar != nil {
		for _, cookie := range s.client.Jar.Cookies(parsed) {
			if cookie.Name != "" {
				values[cookie.Name] = cookie.Value
			}
		}
	}
	for _, cookie := range parseBilibiliCookieHeader(s.cookieHeader()) {
		values[cookie.Name] = cookie.Value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

func parseBilibiliCookieHeader(raw string) []*http.Cookie {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	req := &http.Request{Header: http.Header{"Cookie": []string{raw}}}
	return req.Cookies()
}

func (s *bilibiliService) status() map[string]any {
	ffmpegReady := false
	if filepath.IsAbs(s.ffmpeg) {
		if info, err := os.Stat(s.ffmpeg); err == nil && !info.IsDir() {
			ffmpegReady = true
		}
	} else if _, err := exec.LookPath(s.ffmpeg); err == nil {
		ffmpegReady = true
	}
	files := 0
	if entries, readErr := os.ReadDir(s.dir); readErr == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".mp3") {
				files++
			}
		}
	}
	musicFiles := 0
	if entries, readErr := os.ReadDir(s.musicDir); readErr == nil {
		for _, entry := range entries {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if !entry.IsDir() && (ext == ".mp3" || ext == ".flac" || ext == ".m4a") {
				musicFiles++
			}
		}
	}
	return map[string]any{
		"enabled":      true,
		"ffmpeg_ready": ffmpegReady,
		"ffmpeg":       filepath.Base(s.ffmpeg),
		"cached_files": files,
		"music_files":  musicFiles,
		"logged_in":    s.cookieHeader() != "",
		"nickname":     s.store.nickname(),
		"saved_at":     s.store.savedAt(),
	}
}

func (s *bilibiliService) createQR(ctx context.Context) (bilibiliQR, error) {
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			URL       string `json:"url"`
			QRCodeKey string `json:"qrcode_key"`
		} `json:"data"`
	}
	if err := s.requestJSON(
		ctx,
		"https://passport.bilibili.com/x/passport-login/web/qrcode/generate",
		"https://www.bilibili.com/",
		&response,
	); err != nil {
		return bilibiliQR{}, err
	}
	if response.Code != 0 || response.Data.QRCodeKey == "" || response.Data.URL == "" {
		return bilibiliQR{}, fmt.Errorf("申请 B 站二维码失败（%d）：%s", response.Code, response.Message)
	}
	return bilibiliQR{Key: response.Data.QRCodeKey, URL: response.Data.URL, State: "waiting"}, nil
}

func (s *bilibiliService) pollQR(ctx context.Context, key string) (bilibiliQR, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return bilibiliQR{}, fmt.Errorf("缺少二维码 key")
	}
	params := url.Values{"qrcode_key": {key}}
	rawURL := s.apiURL("https://passport.bilibili.com/x/passport-login/web/qrcode/poll", params)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return bilibiliQR{}, err
	}
	req.Header.Set("User-Agent", bilibiliUserAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	resp, err := s.client.Do(req)
	if err != nil {
		return bilibiliQR{}, fmt.Errorf("查询 B 站扫码状态失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return bilibiliQR{}, err
	}
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			URL     string `json:"url"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return bilibiliQR{}, fmt.Errorf("解析 B 站扫码状态失败: %w", err)
	}
	if response.Code != 0 {
		return bilibiliQR{}, fmt.Errorf("查询 B 站扫码状态失败（%d）：%s", response.Code, response.Message)
	}
	out := bilibiliQR{Key: key, Code: response.Data.Code}
	switch response.Data.Code {
	case 0:
		cookie := cookieFromBilibiliResponse(resp, response.Data.URL)
		if cookie == "" {
			return bilibiliQR{}, fmt.Errorf("B 站登录成功但没有返回 Cookie")
		}
		// 先保存 Cookie，再用它查询昵称。
		if err := s.store.save(cookie, ""); err != nil {
			return bilibiliQR{}, fmt.Errorf("保存 B 站登录态失败: %w", err)
		}
		nickname := ""
		if account, err := s.account(ctx); err == nil {
			nickname = account.Nickname
			_ = s.store.save(cookie, nickname)
		}
		out.State = "confirmed"
		out.Nickname = nickname
	case 86101:
		out.State = "waiting"
	case 86090:
		out.State = "scanned"
	case 86038:
		out.State = "expired"
	default:
		return bilibiliQR{}, fmt.Errorf("B 站扫码状态异常（%d）：%s", response.Data.Code, response.Data.Message)
	}
	return out, nil
}

type bilibiliAccount struct {
	LoggedIn bool
	Nickname string
	MID      int64
}

func (s *bilibiliService) account(ctx context.Context) (bilibiliAccount, error) {
	if strings.TrimSpace(s.cookieHeader()) == "" {
		return bilibiliAccount{}, fmt.Errorf("B 站未登录")
	}
	response, err := s.fetchNav(ctx)
	if err != nil {
		return bilibiliAccount{}, err
	}
	if response.Code != 0 || !response.Data.IsLogin {
		return bilibiliAccount{}, fmt.Errorf("B 站登录态无效")
	}
	return bilibiliAccount{
		LoggedIn: true,
		Nickname: cleanBilibiliText(response.Data.Uname),
		MID:      response.Data.MID,
	}, nil
}

type bilibiliNavResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		IsLogin bool   `json:"isLogin"`
		Uname   string `json:"uname"`
		MID     int64  `json:"mid"`
		WBIImg  struct {
			ImgURL string `json:"img_url"`
			SubURL string `json:"sub_url"`
		} `json:"wbi_img"`
	} `json:"data"`
}

func (s *bilibiliService) fetchNav(ctx context.Context) (bilibiliNavResponse, error) {
	var response bilibiliNavResponse
	if err := s.requestJSON(
		ctx,
		"https://api.bilibili.com/x/web-interface/nav",
		"https://www.bilibili.com/",
		&response,
	); err != nil {
		return bilibiliNavResponse{}, err
	}
	return response, nil
}

func cookieFromBilibiliResponse(resp *http.Response, callbackURL string) string {
	values := map[string]string{}
	for _, cookie := range resp.Cookies() {
		if cookie.Name != "" {
			values[cookie.Name] = cookie.Value
		}
	}
	if parsed, err := url.Parse(callbackURL); err == nil {
		for _, name := range []string{"SESSDATA", "bili_jct", "DedeUserID", "DedeUserID__ckMd5"} {
			if value := parsed.Query().Get(name); value != "" {
				values[name] = value
			}
		}
	}
	order := []string{"SESSDATA", "bili_jct", "DedeUserID", "DedeUserID__ckMd5", "sid"}
	seen := map[string]bool{}
	parts := make([]string, 0, len(values))
	for _, name := range order {
		if value := values[name]; value != "" {
			parts = append(parts, name+"="+value)
			seen[name] = true
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

func (s *bilibiliService) apiURL(base string, params url.Values) string {
	return base + "?" + params.Encode()
}

func (s *bilibiliService) requestJSON(ctx context.Context, rawURL, referer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	s.setRequestHeaders(req, referer)
	if cookie := s.requestCookie(rawURL); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 B 站失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("读取 B 站响应失败: %w", err)
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("B 站风控拦截（HTTP 412），请稍后再试或检查服务器网络环境")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("B 站返回 HTTP %d: %s", resp.StatusCode, shortenText(string(body), 180))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 B 站响应失败: %w", err)
	}
	return nil
}

func (s *bilibiliService) setRequestHeaders(req *http.Request, referer string) {
	req.Header.Set("User-Agent", bilibiliUserAgent)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://www.bilibili.com")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-CH-UA", `"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", `"Windows"`)
}

func (s *bilibiliService) search(ctx context.Context, query string) ([]bilibiliSearchItem, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("搜索关键词不能为空")
	}
	if len([]rune(query)) > 100 {
		return nil, fmt.Errorf("搜索关键词过长")
	}
	s.searchMu.Lock()
	if cached, ok := s.searchCache[query]; ok && time.Since(cached.SavedAt) < 90*time.Second {
		s.searchMu.Unlock()
		return append([]bilibiliSearchItem(nil), cached.Items...), nil
	}
	s.searchMu.Unlock()
	if err := s.ensureAnonymousSession(ctx); err != nil {
		return nil, err
	}
	items, err := s.searchPage(ctx, query)
	if err != nil && isBilibiliRiskError(err) {
		s.resetAnonymousSession()
		if refreshErr := s.ensureAnonymousSession(ctx); refreshErr == nil {
			items, err = s.searchPage(ctx, query)
		}
	}
	if err != nil {
		return nil, err
	}
	items = rankBilibiliItems(query, items)
	if len(items) > s.searchLimit {
		items = items[:s.searchLimit]
	}
	s.searchMu.Lock()
	s.searchCache[query] = bilibiliSearchCache{
		Items:   append([]bilibiliSearchItem(nil), items...),
		SavedAt: time.Now(),
	}
	s.searchMu.Unlock()
	return items, nil
}

func (s *bilibiliService) searchPage(ctx context.Context, query string) ([]bilibiliSearchItem, error) {
	params := url.Values{
		"Search_key":  {query},
		"keyword":     {query},
		"page":        {"1"},
		"context":     {""},
		"duration":    {"0"},
		"tids_2":      {""},
		"__refresh__": {"true"},
		"search_type": {"video"},
		"tids":        {"0"},
		"highlight":   {"1"},
	}
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Result []struct {
				BVID     string `json:"bvid"`
				AID      int64  `json:"aid"`
				Title    string `json:"title"`
				Author   string `json:"author"`
				Duration string `json:"duration"`
				Pic      string `json:"pic"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := s.requestJSON(
		ctx,
		s.apiURL("https://api.bilibili.com/x/web-interface/search/type", params),
		"https://www.bilibili.com/",
		&response,
	); err != nil {
		return nil, err
	}
	if response.Code != 0 {
		if response.Code == -412 {
			return nil, fmt.Errorf("B 站风控拦截（-412）：%s", response.Message)
		}
		return nil, fmt.Errorf("B 站搜索失败（%d）：%s", response.Code, response.Message)
	}
	items := make([]bilibiliSearchItem, 0, len(response.Data.Result))
	for _, result := range response.Data.Result {
		if strings.TrimSpace(result.BVID) == "" {
			continue
		}
		seconds := parseBilibiliDuration(result.Duration)
		item := bilibiliSearchItem{
			BVID:            result.BVID,
			AID:             result.AID,
			Title:           cleanBilibiliText(result.Title),
			Author:          cleanBilibiliText(result.Author),
			Duration:        result.Duration,
			DurationSeconds: seconds,
			Cover:           normalizeBilibiliImage(result.Pic),
			URL:             "https://www.bilibili.com/video/" + result.BVID,
		}
		items = append(items, item)
	}
	return items, nil
}

func isBilibiliRiskError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "412") || strings.Contains(message, "风控")
}

func (s *bilibiliService) ensureAnonymousSession(ctx context.Context) error {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	if s.bootstrapped {
		return nil
	}
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			B3 string `json:"b_3"`
			B4 string `json:"b_4"`
		} `json:"data"`
	}
	spiErr := s.requestJSON(
		ctx,
		"https://api.bilibili.com/x/frontend/finger/spi",
		"https://www.bilibili.com/",
		&response,
	)
	base, _ := url.Parse("https://api.bilibili.com/")
	cookies := []*http.Cookie{
		{Name: "buvid3", Domain: ".bilibili.com", Path: "/"},
		{Name: "buvid_fp", Value: bilibiliBuvidFingerprint(bilibiliUserAgent, 31), Domain: ".bilibili.com", Path: "/"},
		{Name: "_uuid", Value: randomBilibiliUUID(), Domain: ".bilibili.com", Path: "/"},
		{Name: "b_nut", Value: strconv.FormatInt(time.Now().Unix(), 10), Domain: ".bilibili.com", Path: "/"},
		{Name: "b_lsid", Value: randomBilibiliLSID(), Domain: ".bilibili.com", Path: "/"},
		{Name: "CURRENT_FNVAL", Value: "4048", Domain: ".bilibili.com", Path: "/"},
		{Name: "CURRENT_QUALITY", Value: "0", Domain: ".bilibili.com", Path: "/"},
	}
	if spiErr == nil && response.Code == 0 && response.Data.B3 != "" {
		cookies[0].Value = response.Data.B3
	}
	if cookies[0].Value == "" {
		// SPI 被风控时仍给搜索一个匿名设备号；yt-dlp 也用随机 UUID 兜底。
		cookies[0].Value = randomBilibiliUUID() + "infoc"
	}
	if spiErr == nil && response.Code == 0 && response.Data.B4 != "" {
		cookies = append(cookies, &http.Cookie{Name: "buvid4", Value: response.Data.B4, Domain: ".bilibili.com", Path: "/"})
	}
	s.client.Jar.SetCookies(base, cookies)
	s.bootstrapped = true
	return nil
}

func (s *bilibiliService) resetAnonymousSession() {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	s.bootstrapped = false
	if s.client == nil || s.client.Jar == nil {
		return
	}
	base, _ := url.Parse("https://api.bilibili.com/")
	for _, name := range []string{"buvid3", "buvid4"} {
		s.client.Jar.SetCookies(base, []*http.Cookie{{
			Name: name, Value: "", Domain: ".bilibili.com", Path: "/", MaxAge: -1,
		}})
	}
}

func randomBilibiliUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16],
	)
}

func randomBilibiliLSID() string {
	var raw [4]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("%08X_%X", raw, time.Now().Unix())
}

func bilibiliBuvidFingerprint(userAgent string, seed uint64) string {
	// B 站网页端使用 MurmurHash3 x64 128 生成 buvid_fp；Go 侧按无符号 64 位
	// 自然溢出实现，和网页算法的模 2^64 行为一致。
	const (
		c1 = uint64(0x87c37b91114253d5)
		c2 = uint64(0x4cf5ad432745937f)
	)
	rotl := func(value uint64, bits uint) uint64 {
		return value<<bits | value>>(64-bits)
	}
	fmix := func(value uint64) uint64 {
		value ^= value >> 33
		value *= 0xff51afd7ed558ccd
		value ^= value >> 33
		value *= 0xc4ceb9fe1a85ec53
		value ^= value >> 33
		return value
	}
	data := []byte(userAgent)
	h1, h2 := seed, seed
	length := len(data)
	for len(data) >= 16 {
		k1 := uint64(data[0]) | uint64(data[1])<<8 | uint64(data[2])<<16 | uint64(data[3])<<24 |
			uint64(data[4])<<32 | uint64(data[5])<<40 | uint64(data[6])<<48 | uint64(data[7])<<56
		k2 := uint64(data[8]) | uint64(data[9])<<8 | uint64(data[10])<<16 | uint64(data[11])<<24 |
			uint64(data[12])<<32 | uint64(data[13])<<40 | uint64(data[14])<<48 | uint64(data[15])<<56
		h1 ^= rotl(k1*c1, 31) * c2
		h1 = (rotl(h1, 27) + h2) * 5
		h1 += 0x52dce729
		h2 ^= rotl(k2*c2, 33) * c1
		h2 = (rotl(h2, 31) + h1) * 5
		h2 += 0x38495ab5
		data = data[16:]
	}
	var k1, k2 uint64
	for i := len(data) - 1; i >= 0; i-- {
		if i >= 8 {
			k2 ^= uint64(data[i]) << (uint(i-8) * 8)
		} else {
			k1 ^= uint64(data[i]) << (uint(i) * 8)
		}
	}
	if len(data) > 8 {
		h2 ^= rotl(k2*c2, 33) * c1
	}
	if len(data) > 0 {
		h1 ^= rotl(k1*c1, 31) * c2
	}
	h1 ^= uint64(length)
	h2 ^= uint64(length)
	h1 += h2
	h2 += h1
	h1 = fmix(h1)
	h2 = fmix(h2)
	h1 += h2
	h2 += h1
	return strconv.FormatUint(h1, 16) + strconv.FormatUint(h2, 16)
}

func cleanBilibiliText(value string) string {
	value = htmlTagPattern.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	return strings.Join(strings.Fields(value), " ")
}

func normalizeBilibiliImage(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	return value
}

func rankBilibiliItems(query string, items []bilibiliSearchItem) []bilibiliSearchItem {
	query = strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(query)
	for i := range items {
		title := strings.ToLower(items[i].Title)
		score := 0
		if query != "" && strings.Contains(title, query) {
			score += 60
		}
		for _, word := range words {
			if word != "" && strings.Contains(title, word) {
				score += 18
			}
		}
		for _, term := range []string{"无损", "hi-res", "hires", "高音质", "hifi", "母带"} {
			if strings.Contains(title, term) {
				score += 14
			}
		}
		for _, term := range []string{"官方", "mv", "音乐"} {
			if strings.Contains(title, term) {
				score += 5
			}
		}
		for _, term := range []string{"合集", "精选", "100首", "全专辑", "循环", "歌单", "串烧"} {
			if strings.Contains(title, term) {
				score -= 24
			}
		}
		for _, term := range []string{"教程", "鼓谱", "伴奏", "ktv", "翻唱", "钢琴"} {
			if strings.Contains(title, term) {
				score -= 18
			}
		}
		switch seconds := items[i].DurationSeconds; {
		case seconds >= 120 && seconds <= 480:
			score += 16
		case seconds > 1200:
			score -= 16
		case seconds > 600:
			score -= 8
		}
		items[i].Score = score
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return items[i].DurationSeconds < items[j].DurationSeconds
		}
		return items[i].Score > items[j].Score
	})
	for i := range items {
		items[i].Recommended = i == 0
	}
	return items
}

func parseBilibiliDuration(value string) int {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0
	}
	total := 0
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

func bilibiliWBIKey(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	return strings.TrimSuffix(name, path.Ext(name))
}

func bilibiliMixinKey(imgKey, subKey string) (string, error) {
	raw := imgKey + subKey
	if len(raw) < 64 {
		return "", fmt.Errorf("B 站 WBI 签名密钥不完整")
	}
	var key strings.Builder
	key.Grow(32)
	for _, index := range bilibiliMixinKeyEncTab {
		if index < 0 || index >= len(raw) {
			return "", fmt.Errorf("B 站 WBI 签名密钥格式异常")
		}
		key.WriteByte(raw[index])
		if key.Len() == 32 {
			break
		}
	}
	return key.String(), nil
}

func signBilibiliWBI(params url.Values, mixinKey string, now time.Time) url.Values {
	signed := url.Values{}
	for key, values := range params {
		for _, value := range values {
			value = strings.Map(func(r rune) rune {
				if strings.ContainsRune("!'()*", r) {
					return -1
				}
				return r
			}, value)
			signed.Add(key, value)
		}
	}
	signed.Set("wts", strconv.FormatInt(now.Unix(), 10))
	query := signed.Encode()
	sum := md5.Sum([]byte(query + mixinKey))
	signed.Set("w_rid", fmt.Sprintf("%x", sum[:]))
	return signed
}

func (s *bilibiliService) ensureWBI(ctx context.Context) error {
	s.wbiMu.Lock()
	defer s.wbiMu.Unlock()
	if s.wbiImgKey != "" && s.wbiSubKey != "" && time.Since(s.wbiFetchedAt) < 10*time.Minute {
		return nil
	}
	response, err := s.fetchNav(ctx)
	if err != nil {
		return fmt.Errorf("获取 B 站 WBI 签名: %w", err)
	}
	imgKey := bilibiliWBIKey(response.Data.WBIImg.ImgURL)
	subKey := bilibiliWBIKey(response.Data.WBIImg.SubURL)
	if imgKey == "" || subKey == "" {
		message := strings.TrimSpace(response.Message)
		if message == "" {
			message = "未返回签名密钥"
		}
		return fmt.Errorf("获取 B 站 WBI 签名失败：%s", message)
	}
	if _, err := bilibiliMixinKey(imgKey, subKey); err != nil {
		return err
	}
	s.wbiImgKey, s.wbiSubKey, s.wbiFetchedAt = imgKey, subKey, time.Now()
	return nil
}

func (s *bilibiliService) signedWBIURL(ctx context.Context, base string, params url.Values) (string, error) {
	if err := s.ensureWBI(ctx); err != nil {
		return "", err
	}
	s.wbiMu.Lock()
	mixinKey, err := bilibiliMixinKey(s.wbiImgKey, s.wbiSubKey)
	s.wbiMu.Unlock()
	if err != nil {
		return "", err
	}
	return s.apiURL(base, signBilibiliWBI(params, mixinKey, time.Now())), nil
}

func (s *bilibiliService) resolve(ctx context.Context, bvid string, cid int64) (bilibiliVideo, error) {
	if !strings.HasPrefix(strings.TrimSpace(bvid), "BV") {
		return bilibiliVideo{}, fmt.Errorf("bvid 不合法")
	}
	view, err := s.readVideoView(ctx, strings.TrimSpace(bvid))
	if err != nil {
		return bilibiliVideo{}, err
	}
	referer := "https://www.bilibili.com/video/" + bvid
	video := bilibiliVideo{
		BVID:   view.Data.BVID,
		AID:    view.Data.AID,
		CID:    view.Data.CID,
		Title:  cleanBilibiliText(view.Data.Title),
		Author: cleanBilibiliText(view.Data.Owner.Name),
		Cover:  normalizeBilibiliImage(view.Data.Pic),
	}
	if cid > 0 {
		for _, page := range view.Data.Pages {
			if page.CID == cid {
				video.CID = page.CID
				video.PageName = cleanBilibiliText(page.Part)
				video.Duration = page.Duration
				break
			}
		}
		if video.CID != cid {
			return bilibiliVideo{}, fmt.Errorf("视频里没有这个分 P")
		}
	} else if len(view.Data.Pages) > 0 {
		video.CID = view.Data.Pages[0].CID
		video.PageName = cleanBilibiliText(view.Data.Pages[0].Part)
		video.Duration = view.Data.Pages[0].Duration
	}
	if video.CID == 0 {
		return bilibiliVideo{}, fmt.Errorf("视频没有可播放分 P")
	}

	playParams := url.Values{
		"bvid":  {video.BVID},
		"cid":   {strconv.FormatInt(video.CID, 10)},
		"qn":    {"0"},
		"fnver": {"0"},
		"fnval": {"4048"},
		"fourk": {"1"},
	}
	var play struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Dash struct {
				Audio bilibiliAudioTracks `json:"audio"`
				Dolby struct {
					Audio bilibiliAudioTracks `json:"audio"`
				} `json:"dolby"`
				Flac struct {
					Audio bilibiliAudioTracks `json:"audio"`
				} `json:"flac"`
			} `json:"dash"`
		} `json:"data"`
	}
	playURL, err := s.signedWBIURL(ctx, "https://api.bilibili.com/x/player/wbi/playurl", playParams)
	if err != nil {
		return bilibiliVideo{}, err
	}
	if err := s.requestJSON(
		ctx,
		playURL,
		referer,
		&play,
	); err != nil {
		return bilibiliVideo{}, err
	}
	if play.Code != 0 {
		return bilibiliVideo{}, fmt.Errorf("获取音轨失败（%d）：%s", play.Code, play.Message)
	}
	audio := append([]bilibiliAudioTrack(nil), play.Data.Dash.Audio...)
	if len(play.Data.Dash.Dolby.Audio) > 0 {
		audio = append(audio, play.Data.Dash.Dolby.Audio...)
	}
	if len(play.Data.Dash.Flac.Audio) > 0 {
		audio = append(audio, play.Data.Dash.Flac.Audio...)
	}
	if len(audio) == 0 {
		return bilibiliVideo{}, fmt.Errorf("该视频没有可用音轨")
	}
	sort.SliceStable(audio, func(i, j int) bool {
		left, right := bilibiliAudioRank(audio[i]), bilibiliAudioRank(audio[j])
		if left == right {
			return audio[i].Bandwidth > audio[j].Bandwidth
		}
		return left > right
	})
	video.AudioURL = audio[0].BaseURL
	video.AudioBackups = append([]string(nil), audio[0].BackupURL...)
	video.AudioID = audio[0].ID
	video.AudioBandwidth = audio[0].Bandwidth
	video.AudioCodec = audio[0].Codecs
	if video.AudioURL == "" {
		return bilibiliVideo{}, fmt.Errorf("B 站没有返回音轨地址")
	}
	return video, nil
}

func bilibiliAudioRank(track bilibiliAudioTrack) int {
	codec := strings.ToLower(track.Codecs)
	switch {
	case track.ID == 30251 || strings.Contains(codec, "flac"):
		return 3
	case track.ID == 30250 || track.ID == 30255 || strings.Contains(codec, "ec-3") || strings.Contains(codec, "eac3"):
		return 2
	default:
		return 1
	}
}

func (s *bilibiliService) cachedName(video bilibiliVideo) string {
	return fmt.Sprintf("%s-%d.mp3", video.BVID, video.CID)
}

func (s *bilibiliService) cachedSourceName(video bilibiliVideo) string {
	if bilibiliAudioRank(bilibiliAudioTrack{ID: video.AudioID, Codecs: video.AudioCodec}) == 3 {
		return fmt.Sprintf("%s-%d.flac", video.BVID, video.CID)
	}
	return ""
}

func (s *bilibiliService) prepare(ctx context.Context, bvid string, cid int64) (bilibiliCachedAudio, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return bilibiliCachedAudio{}, err
	}

	video, err := s.resolve(ctx, bvid, cid)
	if err != nil {
		return bilibiliCachedAudio{}, err
	}
	name := s.cachedName(video)
	target := filepath.Join(s.dir, name)
	sourceName := s.cachedSourceName(video)
	sourcePath := ""
	sourceReady := false
	if sourceName != "" {
		candidate := filepath.Join(s.dir, sourceName)
		if info, err := os.Stat(candidate); err == nil && info.Size() > 0 {
			sourcePath, sourceReady = candidate, true
		}
	}
	if info, err := os.Stat(target); err == nil && info.Size() > 0 {
		return bilibiliCachedAudio{Video: video, FileName: name, Path: target, SourcePath: sourcePath}, nil
	}
	if err := s.validateFFmpeg(); err != nil {
		return bilibiliCachedAudio{}, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	inputPath := sourcePath
	if inputPath == "" {
		inputPath, err = s.downloadAudioInput(requestCtx, video)
		if err != nil {
			return bilibiliCachedAudio{}, err
		}
		defer os.Remove(inputPath)
	}
	if sourceName != "" && !sourceReady {
		sourcePath = filepath.Join(s.dir, sourceName)
		if err := s.remuxFLAC(requestCtx, inputPath, sourcePath); err != nil {
			return bilibiliCachedAudio{}, err
		}
		sourceReady = true
		inputPath = sourcePath
	}
	if err := s.convertMP3(requestCtx, inputPath, target); err != nil {
		return bilibiliCachedAudio{}, err
	}
	if !sourceReady {
		sourcePath = ""
	}
	return bilibiliCachedAudio{Video: video, FileName: name, Path: target, SourcePath: sourcePath}, nil
}

func (s *bilibiliService) validateFFmpeg() error {
	if filepath.IsAbs(s.ffmpeg) {
		if info, err := os.Stat(s.ffmpeg); err != nil || info.IsDir() {
			return fmt.Errorf("找不到 ffmpeg，请在配置中填写正确路径")
		}
	} else if _, err := exec.LookPath(s.ffmpeg); err != nil {
		return fmt.Errorf("找不到 ffmpeg，请在配置中填写路径")
	}
	return nil
}

func (s *bilibiliService) downloadAudioInput(ctx context.Context, video bilibiliVideo) (string, error) {
	urls := append([]string{video.AudioURL}, video.AudioBackups...)
	var lastErr error
	for _, rawURL := range urls {
		rawURL = normalizeBilibiliImage(rawURL)
		if rawURL == "" {
			continue
		}
		target, err := s.downloadAudioURL(ctx, rawURL, video)
		if err == nil {
			return target, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("B 站没有返回可用音轨地址")
}

func (s *bilibiliService) downloadAudioURL(ctx context.Context, rawURL string, video bilibiliVideo) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	s.setRequestHeaders(req, "https://www.bilibili.com/video/"+video.BVID)
	req.Header.Set("Accept", "*/*")
	if cookie := s.requestCookie(rawURL); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := s.streamClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 B 站音轨失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 B 站音轨失败：HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > s.maxBytes {
		return "", fmt.Errorf("音轨超过 %d MB 限制", s.maxBytes>>20)
	}
	input, err := os.CreateTemp(s.dir, ".bilibili-*.m4s")
	if err != nil {
		return "", err
	}
	inputPath := input.Name()
	written, copyErr := io.Copy(input, io.LimitReader(resp.Body, s.maxBytes+1))
	closeErr := input.Close()
	if copyErr != nil {
		_ = os.Remove(inputPath)
		return "", fmt.Errorf("保存音轨失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(inputPath)
		return "", closeErr
	}
	if written > s.maxBytes {
		_ = os.Remove(inputPath)
		return "", fmt.Errorf("音轨超过 %d MB 限制", s.maxBytes>>20)
	}
	if written == 0 {
		_ = os.Remove(inputPath)
		return "", fmt.Errorf("B 站返回了空音轨")
	}
	return inputPath, nil
}

func (s *bilibiliService) remuxFLAC(ctx context.Context, inputPath, target string) error {
	tempOut := target + ".tmp.flac"
	_ = os.Remove(tempOut)
	cmd := exec.CommandContext(
		ctx,
		s.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", inputPath,
		"-map", "0:a:0", "-vn", "-c:a", "copy", "-f", "flac",
		tempOut,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tempOut)
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ffmpeg 封装无损音轨失败: %s", shortenText(message, 240))
	}
	if info, err := os.Stat(tempOut); err != nil || info.Size() == 0 {
		_ = os.Remove(tempOut)
		if err == nil {
			err = fmt.Errorf("输出文件为空")
		}
		return fmt.Errorf("ffmpeg 没有生成无损音频: %w", err)
	}
	if err := os.Rename(tempOut, target); err != nil {
		_ = os.Remove(tempOut)
		return err
	}
	return nil
}

func (s *bilibiliService) convertMP3(ctx context.Context, inputPath, target string) error {
	tempOut := target + ".tmp.mp3"
	_ = os.Remove(tempOut)
	cmd := exec.CommandContext(
		ctx,
		s.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", inputPath,
		"-map", "0:a:0", "-vn", "-c:a", "libmp3lame", "-b:a", "320k",
		tempOut,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tempOut)
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ffmpeg 转换失败: %s", shortenText(message, 240))
	}
	if info, err := os.Stat(tempOut); err != nil || info.Size() == 0 {
		_ = os.Remove(tempOut)
		if err == nil {
			err = fmt.Errorf("输出文件为空")
		}
		return fmt.Errorf("ffmpeg 没有生成有效音频: %w", err)
	}
	if err := os.Rename(tempOut, target); err != nil {
		_ = os.Remove(tempOut)
		return err
	}
	return nil
}

func safeBilibiliMusicName(video bilibiliVideo, ext string) string {
	title := strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, video.Title)
	title = strings.Trim(strings.TrimSpace(title), ". ")
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80])
	}
	if title == "" {
		title = "B站音乐"
	}
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		ext = ".mp3"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return fmt.Sprintf("%s [%s-%d]%s", title, video.BVID, video.CID, ext)
}

func (s *bilibiliService) saveToMusic(cached bilibiliCachedAudio) (string, string, error) {
	sourcePath := cached.SourcePath
	if sourcePath == "" {
		sourcePath = cached.Path
	}
	ext := strings.ToLower(filepath.Ext(sourcePath))
	if ext != ".flac" && ext != ".mp3" {
		ext = ".mp3"
	}
	name := safeBilibiliMusicName(cached.Video, ext)
	target := filepath.Join(s.musicDir, name)
	if info, err := os.Stat(target); err == nil && info.Size() > 0 {
		return target, name, nil
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", "", fmt.Errorf("打开缓存音频失败: %w", err)
	}
	defer source.Close()
	temp, err := os.CreateTemp(s.musicDir, ".bilibili-music-*.tmp")
	if err != nil {
		return "", "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(temp, source); err != nil {
		_ = temp.Close()
		return "", "", err
	}
	if err := temp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return "", "", err
	}
	return target, name, nil
}

func (s *bilibiliService) audioPath(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	if name == "." || name == "" || !strings.HasSuffix(strings.ToLower(name), ".mp3") {
		return "", false
	}
	target := filepath.Join(s.dir, name)
	info, err := os.Stat(target)
	return target, err == nil && !info.IsDir()
}
