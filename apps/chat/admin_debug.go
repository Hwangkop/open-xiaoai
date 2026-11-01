package main

// 临时调试接口：在音箱上执行 shell 命令（只允许本机访问）。
// 仅用于排查音频设备/AEC/音量控制，验证完可以删掉。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (a *adminServer) handleDebugShell(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	var req struct {
		Cmd       string `json:"cmd"`
		TimeoutMs uint64 `json:"timeout_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Cmd == "" {
		writeAdminError(w, http.StatusBadRequest, "需要 cmd")
		return
	}
	if req.TimeoutMs == 0 || req.TimeoutMs > 20000 {
		req.TimeoutMs = 8000
	}
	res, err := a.speaker.runShell(req.Cmd, req.TimeoutMs)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"stdout":    res.Stdout,
		"stderr":    res.Stderr,
		"exit_code": res.ExitCode,
	})
}

// handleDebugMicCapture 录一段原始 mic PCM（16k/mono/S16_LE），落盘到 config 同目录。
// AEC 探针和离线调参用：拿它和"同一时刻在播的音频"做互相关，就能知道设备输出的
// 线性程度和真实延迟。GET /admin/api/debug/mic-capture?seconds=20（只允许本机）
func (a *adminServer) handleDebugMicCapture(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}

	seconds := 10
	if v := r.URL.Query().Get("seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			seconds = n
		}
	}
	if seconds > 60 {
		seconds = 60
	}

	name := fmt.Sprintf("mic-capture-%s.raw", time.Now().Format("20060102-150405"))
	path := filepath.Join(filepath.Dir(a.configPath), name)
	f, err := os.Create(path)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("创建文件失败：%v", err))
		return
	}

	var mu sync.Mutex
	var got int64
	closed := false
	stop := micListener.SetTap(func(pcm []byte) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		n, _ := f.Write(pcm)
		got += int64(n)
	})

	// 记下开始那一刻的设备状态，后面算参考信号要用
	statusOut, _ := a.speaker.runShell("ubus -t 2 call mediaplayer player_get_play_status", 3000)
	status := 0
	volume := 0
	if statusOut != nil {
		status = mediaPlayerStatus(statusOut.Stdout)
		if v, ok := mediaPlayerVolume(statusOut.Stdout); ok {
			volume = v
		}
	}

	time.Sleep(time.Duration(seconds) * time.Second)

	stop()
	mu.Lock()
	closed = true
	mu.Unlock()
	_ = f.Close()

	log.Printf("🎙️ mic 录制完成: %s（%d 字节 ≈ %.1f 秒）", name, got, float64(got)/32000)
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"path":        path,
		"name":        name,
		"bytes":       got,
		"seconds":     seconds,
		"sample_rate": micSampleRate,
		"status":      status, // 0 空闲 / 1 播放 / 2 暂停
		"volume":      volume,
		"music":       a.speaker.MusicPlaying(),
	})
}

// handleDebugDoubaoChat 直接给豆包发一句话并返回完整回复（只允许本机）。
// 用于收集对话风格语料；不走 agent，也不朗读。
func (a *adminServer) handleDebugDoubaoChat(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeAdminError(w, http.StatusBadRequest, "需要 text")
		return
	}
	if a.getDoubaoWeb == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "豆包模块未初始化")
		return
	}
	dw := a.getDoubaoWeb()
	if dw == nil || !dw.LoggedIn() {
		writeAdminError(w, http.StatusServiceUnavailable, "豆包未登录")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	ans, err := dw.ChatStream(ctx, strings.TrimSpace(req.Text), nil)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"reply": ans})
}

// handleDebugDoubaoReset 清掉当前豆包会话，下一条消息会新建会话（只允许本机）。
func (a *adminServer) handleDebugDoubaoReset(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	if a.getDoubaoWeb == nil || a.getDoubaoWeb() == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "豆包模块未初始化")
		return
	}
	a.getDoubaoWeb().ResetConversation()
	writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDebugDoubaoRaw 发一句话并返回原始 SSE（只允许本机，调试流式解析用）。
func (a *adminServer) handleDebugDoubaoRaw(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeAdminError(w, http.StatusBadRequest, "需要 text")
		return
	}
	if a.getDoubaoWeb == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "豆包模块未初始化")
		return
	}
	dw := a.getDoubaoWeb()
	if dw == nil || !dw.LoggedIn() {
		writeAdminError(w, http.StatusServiceUnavailable, "豆包未登录")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	ans, raw, err := dw.ChatCaptureRaw(ctx, strings.TrimSpace(req.Text))
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	if len(raw) > 20000 {
		raw = raw[:20000]
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"reply": ans, "raw": raw})
}

func (a *adminServer) handleDebugUtterance(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Text == "" {
		writeAdminError(w, http.StatusBadRequest, "需要 text")
		return
	}
	if a.engine == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "引擎未就绪")
		return
	}
	a.engine.OnPCMUtterance(req.Text)
	writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDebugMusicState 返回当前播放的歌和它的直链（只允许本机）。
// AEC 探针要用它拿"设备正在播的到底是什么"——参考信号必须和播放的是同一份音频。
func (a *adminServer) handleDebugMusicState(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	a.music.mu.Lock()
	cur := a.music.current
	url := a.music.currentURL
	queueName := a.music.queueName
	idx, total := a.music.idx, len(a.music.queue)
	a.music.mu.Unlock()

	out := map[string]any{
		"music":       a.speaker.MusicPlaying(),
		"position_ms": a.speaker.MusicPositionMs(),
		"volume":      func() int { v, _ := a.speaker.deviceVolume(); return v }(),
		"queue_name":  queueName,
		"index":       idx,
		"total":       total,
		"url":         url,
	}
	if cur != nil {
		out["name"] = cur.Name
		out["artists"] = cur.Artists
		out["id"] = cur.ID
	}
	writeAdminJSON(w, http.StatusOK, out)
}

// handleDebugRoute 只跑音乐态意图路由，不执行任何动作（只允许本机）。
// 用来观察路由器对真实说法的判断质量，调 prompt / 阈值时不用真放音乐。
func (a *adminServer) handleDebugRoute(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeAdminError(w, http.StatusBadRequest, "需要 text")
		return
	}
	if a.engine == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "引擎未就绪")
		return
	}
	song, artists, volume := a.musicDesc()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	text := strings.TrimSpace(req.Text)

	// 1) 工具路由（只问模型，不执行）
	toolCalls, raw, terr := a.engine.previewToolRoute(ctx, text, "")
	toolInfo := map[string]any{"error": ""}
	if terr != nil {
		toolInfo["error"] = terr.Error()
	} else {
		toolInfo["content"] = raw
		var calls []map[string]any
		for _, c := range toolCalls {
			calls = append(calls, map[string]any{"name": c.Name, "arguments": c.Arguments})
		}
		toolInfo["calls"] = calls
	}

	// 2) 轻量分类器
	d, err := a.engine.decideMusicIntent(ctx, text, song, artists, volume)
	if err != nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{"tool_router": toolInfo, "classifier_error": err.Error()})
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"tool_router": toolInfo, "classifier": d})
}

// handleDebugLyric 诊断歌词比对：给一句话，返回它和当前歌曲歌词的接近程度。
// GET /admin/api/debug/lyric?text=xxx（只允许本机）
func (a *adminServer) handleDebugLyric(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
		http.Error(w, "只允许本机访问", http.StatusForbidden)
		return
	}
	text := strings.TrimSpace(r.URL.Query().Get("text"))
	if text == "" {
		writeAdminError(w, http.StatusBadRequest, "需要 text")
		return
	}
	a.music.mu.Lock()
	cur := a.music.current
	a.music.mu.Unlock()
	if cur == nil || a.lyrics == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "当前没有歌曲或歌词未初始化")
		return
	}
	pos := a.speaker.MusicPositionMs()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	lines := a.lyrics.Lines(ctx, cur.ID)

	var d time.Duration = -1
	if pos > 0 {
		d = time.Duration(pos) * time.Millisecond
	}
	hit := lyricExplain(lines, d, text)
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"song":        cur.Name,
		"position_ms": pos,
		"line_count":  len(lines),
		"result":      hit,
	})
}
