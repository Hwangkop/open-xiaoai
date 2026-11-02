package main

// 管理台「音频通道测试」：列出可控制音量的播放通道，逐个试播 TTS，
// 用来判断哪条通道能和正在播放的音乐共存。只用于调试，不参与正式对话路由。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type audioTestPlay struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Device        string `json:"device"`
	VolumeControl string `json:"volume_control"`
	VolumeMax     int    `json:"volume_max"`
	Note          string `json:"note"`
}

type audioTestRequest struct {
	Action  string `json:"action"`
	Op      string `json:"op"`
	Play    string `json:"play"`
	Text    string `json:"text"`
	Volume  *int   `json:"volume"`
	Query   string `json:"query"`
	Enabled *bool  `json:"enabled"`
}

var audioTestPlays = []audioTestPlay{
	{
		ID: "notify", Name: "aplay -D notify", Device: "notify PCM",
		VolumeControl: "notifyvol", VolumeMax: 255,
		Note: "实测：调 notifyvol 只变说话声，音乐不变；TTS 与音乐共存（当前豆包路径）",
	},
	{
		ID: "playback", Name: "aplay -D Playback", Device: "Playback PCM",
		VolumeControl: "mysoftvol", VolumeMax: 255,
		Note: "实测：调 mysoftvol 会同时改变音乐和说话声；TTS 与音乐共存但音量不独立",
	},
	{
		ID: "miplayer", Name: "miplayer -f", Device: "独立播放器",
		VolumeControl: "mysoftvol", VolumeMax: 255,
		Note: "实测：与音乐共存；调 mysoftvol 只变音乐，miplayer 的说话声音量不变",
	},
	{
		ID: "mibrain", Name: "mediaplayer mibrain", Device: "小爱 TTS 队列",
		VolumeControl: "mediaplayer", VolumeMax: 100,
		Note: "实测：音乐会停/被替换，不共存",
	},
	{
		ID: "common", Name: "mediaplayer common", Device: "音乐/主播放器",
		VolumeControl: "mediaplayer", VolumeMax: 100,
		Note: "实测：音乐会停/被替换，不共存",
	},
}

func (a *adminServer) handleAudioTest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.writeAudioTestState(w)
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "只支持 GET/POST", http.StatusMethodNotAllowed)
		return
	}

	var req audioTestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}

	switch req.Action {
	case "music":
		a.handleAudioTestMusic(w, r, req)
	case "tts":
		a.handleAudioTestTTS(w, req)
	case "volume":
		if req.Volume == nil {
			writeAdminError(w, http.StatusBadRequest, "缺少 volume")
			return
		}
		if err := a.applyAudioTestVolume(req.Play, *req.Volume); err != nil {
			writeAdminError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"message": "音量已设置", "play": req.Play, "volume": *req.Volume,
		})
	case "asr":
		if req.Enabled == nil {
			writeAdminError(w, http.StatusBadRequest, "缺少 enabled")
			return
		}
		if a.engine == nil {
			writeAdminError(w, http.StatusInternalServerError, "engine 未初始化")
			return
		}
		a.engine.SetLocalASRDisabled(!*req.Enabled)
		log.Printf("🧪 [audio-test] 本地 ASR 开关: enabled=%v（关闭后忽略 mic/kws）", *req.Enabled)
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"message":           "本地 ASR 已" + map[bool]string{true: "开启", false: "关闭"}[*req.Enabled],
			"local_asr_enabled": *req.Enabled,
		})
	default:
		writeAdminError(w, http.StatusBadRequest, "action 只能是 music / tts / volume / asr")
	}
}

func (a *adminServer) handleAudioTestMusic(w http.ResponseWriter, r *http.Request, req audioTestRequest) {
	switch req.Op {
	case "stop":
		a.stopPlayback()
		log.Printf("🧪 [audio-test] 停止音乐")
		writeAdminJSON(w, http.StatusOK, map[string]any{"message": "音乐已停止"})
	case "start":
		query := strings.TrimSpace(req.Query)
		if query == "" {
			query = "小幸运"
		}
		msg, err := a.searchAndPlay(r.Context(), query, false)
		if err != nil {
			writeAdminError(w, http.StatusBadGateway, "启动音乐失败："+err.Error())
			return
		}
		log.Printf("🧪 [audio-test] 启动音乐 %q: %s", query, msg)
		writeAdminJSON(w, http.StatusOK, map[string]any{"message": msg})
	default:
		writeAdminError(w, http.StatusBadRequest, "op 只能是 start / stop")
	}
}

func (a *adminServer) handleAudioTestTTS(w http.ResponseWriter, req audioTestRequest) {
	play, ok := findAudioTestPlay(req.Play)
	if !ok {
		writeAdminError(w, http.StatusBadRequest, "未知的播放通道: "+req.Play)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeAdminError(w, http.StatusBadRequest, "text 不能为空")
		return
	}
	if a.speaker == nil {
		writeAdminError(w, http.StatusInternalServerError, "speaker 未初始化")
		return
	}
	if req.Volume != nil {
		if err := a.applyAudioTestVolume(play.ID, *req.Volume); err != nil {
			writeAdminError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	start := time.Now()
	url, cached, err := a.speaker.SynthesizeURLWithCache(text)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, "TTS 合成失败："+err.Error())
		return
	}
	script := audioTestPlayScript(play.ID, url)
	res, err := a.speaker.runShell(script, 120000)
	elapsed := time.Since(start).Round(time.Millisecond)
	out := map[string]any{
		"message":    "已发送到 " + play.Name,
		"play":       play.ID,
		"command":    script,
		"elapsed_ms": elapsed.Milliseconds(),
		"ok":         err == nil,
		"cached":     cached,
	}
	if res != nil {
		out["stdout"] = strings.TrimSpace(res.Stdout)
		out["stderr"] = strings.TrimSpace(res.Stderr)
		out["exit_code"] = res.ExitCode
	}
	if err != nil {
		out["error"] = err.Error()
		log.Printf("🧪 [audio-test] %s 试播失败: %v", play.ID, err)
		writeAdminJSON(w, http.StatusOK, out)
		return
	}
	log.Printf("🧪 [audio-test] %s 试播完成（%s，文本 %q）", play.ID, elapsed, text)
	writeAdminJSON(w, http.StatusOK, out)
}

func (a *adminServer) applyAudioTestVolume(playID string, volume int) error {
	play, ok := findAudioTestPlay(playID)
	if !ok {
		return fmt.Errorf("未知的播放通道: %s", playID)
	}
	if volume < 0 {
		volume = 0
	}
	if volume > play.VolumeMax {
		volume = play.VolumeMax
	}
	var script string
	switch play.VolumeControl {
	case "notifyvol":
		script = fmt.Sprintf("amixer -c 0 sset notifyvol %d >/dev/null 2>&1", volume)
	case "mysoftvol":
		script = fmt.Sprintf("amixer -c 0 sset mysoftvol %d >/dev/null 2>&1", volume)
	case "mediaplayer":
		script = fmt.Sprintf(`ubus -t 2 call mediaplayer player_set_volume '{"volume":%d,"beep":0}'`, volume)
	default:
		return fmt.Errorf("通道 %s 没有音量控制", playID)
	}
	if _, err := a.speaker.runShell(script, 4000); err != nil {
		return fmt.Errorf("设置音量失败: %w", err)
	}
	return nil
}

func audioTestPlayScript(playID, url string) string {
	switch playID {
	case "notify":
		return fmt.Sprintf("wget -q -O - '%s' | aplay -D notify -", shellEscapeSingle(url))
	case "playback":
		return fmt.Sprintf("wget -q -O - '%s' | aplay -D Playback -", shellEscapeSingle(url))
	case "miplayer":
		return fmt.Sprintf(
			"wget -q -O /tmp/xiaoai-audio-test.wav '%s' && miplayer -f /tmp/xiaoai-audio-test.wav; rm -f /tmp/xiaoai-audio-test.wav",
			shellEscapeSingle(url))
	case "mibrain":
		payload, _ := json.Marshal(map[string]any{"url": url, "type": 1, "media": "mibrain"})
		return fmt.Sprintf("ubus -t 5 call mediaplayer player_play_url '%s'", shellEscapeSingle(string(payload)))
	case "common":
		payload, _ := json.Marshal(map[string]any{"url": url, "type": 1})
		return fmt.Sprintf("ubus -t 5 call mediaplayer player_play_url '%s'", shellEscapeSingle(string(payload)))
	}
	return ""
}

var audioTestAmixerVolumeRe = regexp.MustCompile(`(?m)Front Right:\s*(\d+)`)

func (a *adminServer) writeAudioTestState(w http.ResponseWriter) {
	plays := make([]map[string]any, 0, len(audioTestPlays))
	for _, p := range audioTestPlays {
		row := map[string]any{
			"id": p.ID, "name": p.Name, "device": p.Device,
			"volume_control": p.VolumeControl, "volume_max": p.VolumeMax,
			"volume": -1, "note": p.Note,
		}
		if v, err := a.audioTestVolume(p); err == nil {
			row["volume"] = v
		}
		plays = append(plays, row)
	}

	playerVolume, _ := a.speaker.deviceVolume()
	a.music.mu.Lock()
	queueName := a.music.queueName
	idx, total := a.music.idx, len(a.music.queue)
	var name, artists string
	if a.music.current != nil {
		name = a.music.current.Name
		artists = a.music.current.Artists
	}
	a.music.mu.Unlock()

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"music": map[string]any{
			"playing":    a.speaker.MusicPlaying(),
			"volume":     playerVolume,
			"queue_name": queueName,
			"index":      idx,
			"total":      total,
			"name":       name,
			"artists":    artists,
		},
		"tts_volume":        a.speaker.TTSVolume(),
		"local_asr_enabled": a.engine == nil || !a.engine.LocalASRDisabled(),
		"plays":             plays,
	})
}

func (a *adminServer) audioTestVolume(p audioTestPlay) (int, error) {
	switch p.VolumeControl {
	case "notifyvol", "mysoftvol":
		res, err := a.speaker.runShell(fmt.Sprintf("amixer -c 0 sget %s", p.VolumeControl), 3000)
		if err != nil || res == nil {
			return 0, err
		}
		m := audioTestAmixerVolumeRe.FindStringSubmatch(res.Stdout)
		if len(m) != 2 {
			return 0, fmt.Errorf("解析音量失败")
		}
		var v int
		if _, err := fmt.Sscanf(m[1], "%d", &v); err != nil {
			return 0, err
		}
		return v, nil
	case "mediaplayer":
		return a.speaker.deviceVolume()
	}
	return 0, fmt.Errorf("通道没有音量控制")
}

func findAudioTestPlay(id string) (audioTestPlay, bool) {
	for _, p := range audioTestPlays {
		if p.ID == id {
			return p, true
		}
	}
	return audioTestPlay{}, false
}
