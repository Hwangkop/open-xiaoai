package main

// 管理端：网易云扫码登录 + 登录态管理。
//
//	GET  /admin/api/netease/status        当前登录态
//	POST /admin/api/netease/qr            取二维码（返回 key + PNG base64）
//	GET  /admin/api/netease/qr?key=xxx    轮询扫码结果；成功后把 cookie 存盘
//
// cookie 存在 netease-session.json，播放时用它取 VIP 音源。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

type neteaseSession struct {
	Cookie  string `json:"cookie"`
	SavedAt int64  `json:"saved_at"`
}

type neteaseStore struct {
	mu   sync.RWMutex
	path string
	sess neteaseSession
}

func newNeteaseStore(configPath string) *neteaseStore {
	dir := filepath.Dir(configPath)
	if dir == "" {
		dir = "."
	}
	s := &neteaseStore{path: filepath.Join(dir, "netease-session.json")}
	s.load()
	return s
}

func (s *neteaseStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var sess neteaseSession
	if json.Unmarshal(raw, &sess) == nil {
		s.sess = sess
	}
}

func (s *neteaseStore) Cookie() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sess.Cookie
}

func (s *neteaseStore) save(cookie string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess = neteaseSession{Cookie: cookie, SavedAt: time.Now().Unix()}
	raw, err := json.MarshalIndent(s.sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}

func (a *adminServer) neteaseClient() *netease.Client {
	cookie := ""
	if a.getNeteaseStore != nil {
		if s := a.getNeteaseStore(); s != nil {
			cookie = s.Cookie()
		}
	}
	return netease.New(cookie)
}

func (a *adminServer) handleNeteaseStatus(w http.ResponseWriter, r *http.Request) {
	cookie := ""
	var savedAt int64
	if a.getNeteaseStore != nil {
		if s := a.getNeteaseStore(); s != nil {
			cookie = s.Cookie()
			savedAt = s.sess.SavedAt
		}
	}
	out := map[string]any{
		"logged_in": cookieValueOf(cookie, "MUSIC_U") != "",
		"cookies":   len(strings.Split(cookie, ";")),
	}
	// 顺便查一下账号信息（昵称/VIP），也用来确认 cookie 是否还有效
	if out["logged_in"].(bool) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if acc, err := a.neteaseClient().GetAccount(ctx); err == nil && acc != nil {
			out["nickname"] = acc.Nickname
			out["vip_type"] = acc.VIPType
			if !acc.LoggedIn {
				out["logged_in"] = false
				out["expired"] = true
			}
		}
	}
	if savedAt > 0 {
		out["saved_at"] = time.Unix(savedAt, 0).Format(time.RFC3339)
	}
	writeAdminJSON(w, http.StatusOK, out)
}

func (a *adminServer) handleNeteaseQR(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		qr, err := a.neteaseClient().CreateQR(ctx)
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"key":    qr.Key,
			"image":  base64.StdEncoding.EncodeToString(qr.Image),
			"status": "qr_ready",
		})

	case http.MethodGet:
		key := strings.TrimSpace(r.URL.Query().Get("key"))
		if key == "" {
			writeAdminError(w, http.StatusBadRequest, "缺少 key 参数")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		st, err := a.neteaseClient().CheckQR(ctx, key)
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, err.Error())
			return
		}
		if st.State == "confirmed" && a.getNeteaseStore != nil {
			if s := a.getNeteaseStore(); s != nil {
				if err := s.save(st.Cookie); err != nil {
					writeAdminError(w, http.StatusInternalServerError, "登录成功但保存失败: "+err.Error())
					return
				}
			}
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"status": st.State, "code": st.Code})

	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

func cookieValueOf(cookie, name string) string {
	for _, part := range strings.Split(cookie, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), name+"="); ok {
			return v
		}
	}
	return ""
}
