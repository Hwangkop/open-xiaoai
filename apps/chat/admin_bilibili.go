package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// B 站音乐管理 API。搜索只返回公开视频；播放/下载由用户明确选择后触发。
//
//	GET  /admin/api/bilibili/status
//	GET  /admin/api/bilibili/search?q=...
//	POST /admin/api/bilibili/play    {"bvid":"BV...","cid":123}
//	POST /admin/api/bilibili/download {"bvid":"BV...","cid":123}
//	GET  /bilibili/audio/{file}.mp3

func (a *adminServer) handleBilibiliStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	if a.bilibili == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "B 站音乐未启用，请在配置页打开 bilibili.enabled 并重启服务",
		})
		return
	}
	status := a.bilibili.status()
	if a.bilibili.cookieHeader() != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if account, err := a.bilibili.account(ctx); err == nil {
			status["logged_in"] = true
			status["nickname"] = account.Nickname
		} else {
			status["logged_in"] = false
			status["expired"] = true
		}
	}
	writeAdminJSON(w, http.StatusOK, status)
}

func (a *adminServer) handleBilibiliQR(w http.ResponseWriter, r *http.Request) {
	if a.bilibili == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "B 站音乐未启用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodPost:
		qr, err := a.bilibili.createQR(ctx)
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, err.Error())
			return
		}
		image, err := qrcode.Encode(qr.URL, qrcode.Medium, 320)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "生成二维码失败: "+err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"key":   qr.Key,
			"url":   qr.URL,
			"image": base64.StdEncoding.EncodeToString(image),
		})
	case http.MethodGet:
		key := strings.TrimSpace(r.URL.Query().Get("key"))
		if key == "" {
			writeAdminError(w, http.StatusBadRequest, "缺少 key")
			return
		}
		qr, err := a.bilibili.pollQR(ctx, key)
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, qr)
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
	}
}

func (a *adminServer) handleBilibiliSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	if a.bilibili == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "B 站音乐未启用")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeAdminError(w, http.StatusBadRequest, "请输入搜索关键词")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	items, err := a.bilibili.search(ctx, query)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"query": query,
		"items": items,
	})
}

func (a *adminServer) handleBilibiliAction(w http.ResponseWriter, r *http.Request, download bool) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	if a.bilibili == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "B 站音乐未启用")
		return
	}
	var req struct {
		BVID string `json:"bvid"`
		CID  int64  `json:"cid"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}
	req.BVID = strings.TrimSpace(req.BVID)
	if req.BVID == "" {
		writeAdminError(w, http.StatusBadRequest, "缺少 bvid")
		return
	}
	if !download {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		message, err := a.playBilibiliVideo(ctx, req.BVID, req.CID)
		if err != nil {
			writeAdminError(w, 400, err.Error())
			return
		}
		writeAdminJSON(w, 202, adminResponse{Message: message})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Minute)
	defer cancel()
	cached, err := a.bilibili.prepare(ctx, req.BVID, req.CID)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	audioPath := "/bilibili/audio/" + url.PathEscape(cached.FileName)
	result := map[string]any{
		"bvid":          cached.Video.BVID,
		"cid":           cached.Video.CID,
		"title":         cached.Video.Title,
		"author":        cached.Video.Author,
		"page":          cached.Video.PageName,
		"duration":      cached.Video.Duration,
		"file_name":     cached.FileName,
		"download_url":  audioPath,
		"audio_quality": fmt.Sprintf("%d kbps · %s", cached.Video.AudioBandwidth/1000, cached.Video.AudioCodec),
	}
	if download {
		_, savedName, err := a.bilibili.saveToMusic(cached)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("保存到音乐目录失败：%v", err))
			return
		}
		result["saved_name"] = savedName
		result["message"] = "音频已永久保存到本地音乐目录"
		writeAdminJSON(w, http.StatusOK, result)
		return
	}

}

func (a *adminServer) handleBilibiliAudio(w http.ResponseWriter, r *http.Request) {
	if a.bilibili == nil {
		http.NotFound(w, r)
		return
	}
	name := path.Base(strings.TrimPrefix(r.URL.Path, "/bilibili/audio/"))
	target, ok := a.bilibili.audioPath(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeFile(w, r, target)
}
