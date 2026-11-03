package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const adminMaxRequestBytes = 2 << 20

type adminServer struct {
	saveMu          sync.Mutex
	configPath      string
	applyConfig     func(*AppConfig) (adminApplyResult, error)
	playTTS         func(string) error
	playURL         func(string) error
	chatTest        func(string)
	tasks           *taskStore
	music           *musicState
	speaker         *Speaker
	engine          *Engine
	runTask         func(string) error
	stopTask        func(string) bool
	stopPlayback    func()
	getDoubaoConfig func() Doubao2APIConfig
	getSchedule     func() *ScheduleWatcher
	getDoubaoWeb    func() *DoubaoWeb
	getASRRouter    func() *asrRouter
	getConfig       func() *AppConfig
	getWakeStore    func() *wakeStore
	getNeteaseStore func() *neteaseStore
	knowledge       *knowledgeService
	audiobooks      *audiobookService
	bilibili        *bilibiliService
	mediaBaseURL    func() string

	lyrics *lyricStore // 音乐态歌词过滤（L3）

	codexServer *CodexAppServer // 智能体页：管理三条 thread 的上下文
	taskRunner  TaskRunner
}

type adminApplyResult struct {
	MusicReloaded   bool `json:"music_reloaded"`
	MusicStopped    bool `json:"music_stopped"`
	RestartRequired bool `json:"restart_required"`
}

type adminConfigRequest struct {
	Content string `json:"content"`
}

type adminConfigResponse struct {
	Path            string `json:"path"`
	Content         string `json:"content,omitempty"`
	Message         string `json:"message"`
	MusicReloaded   bool   `json:"music_reloaded"`
	MusicStopped    bool   `json:"music_stopped"`
	RestartRequired bool   `json:"restart_required"`
}

type adminTTSRequest struct {
	Text string `json:"text"`
}

type adminResponse struct {
	Message string `json:"message"`
}

type adminErrorResponse struct {
	Error string `json:"error"`
}

func newAdminServer(app *appRuntime) *adminServer {
	var a *adminServer
	a = &adminServer{
		music:      newMusicState(),
		speaker:    app.speaker,
		engine:     app.engine,
		configPath: app.configPath,
		applyConfig: func(cfg *AppConfig) (adminApplyResult, error) {
			return app.ApplyConfig(cfg)
		},
		chatTest: func(text string) {
			if app.engine != nil {
				app.engine.OnMessageAuthorized(text)
			}
		},
		playTTS: func(text string) error {
			return app.speaker.PlayTTSWithTimeout(text, 15000)
		},
		playURL: func(url string) error {
			// 直接下发；不能被旧对话的待播清理/取消误删。
			if err := app.speaker.PlayMusicURL(url); err != nil {
				return err
			}
			a.audiobooks.relinquishLocked()
			return nil
		},
		stopPlayback: func() {
			a.music.opMu.Lock()
			defer a.music.opMu.Unlock()
			a.cancelBilibili(false, "stopped")
			a.persistMusic()
			a.audiobooks.relinquishLocked()
			app.speaker.StopMusic()
			_ = app.speaker.StopTTS()
		},
		getDoubaoConfig: func() Doubao2APIConfig {
			if cfg := app.Config(); cfg != nil {
				return cfg.Doubao2API
			}
			return Doubao2APIConfig{}
		},
		getSchedule: func() *ScheduleWatcher {
			return app.ScheduleWatcher()
		},
		getDoubaoWeb: func() *DoubaoWeb {
			return app.DoubaoWeb()
		},
		getASRRouter: func() *asrRouter {
			return app.ASRRouter()
		},
		getConfig: func() *AppConfig {
			return app.Config()
		},
		getWakeStore: func() *wakeStore {
			return app.WakeStore()
		},
		getNeteaseStore: func() *neteaseStore {
			return app.NeteaseStore()
		},
		mediaBaseURL: app.ttsBaseURL,
	}

	a.codexServer = app.codexServer
	a.taskRunner = app.taskRunner
	if cfg := app.Config(); cfg != nil && cfg.Knowledge.Enabled {
		knowledge, err := newKnowledgeService(app.configPath, cfg.Knowledge)
		if err != nil {
			log.Printf("📚 知识库管理未启用: %v", err)
		} else {
			a.knowledge = knowledge
		}
	}
	if cfg := app.Config(); cfg != nil && cfg.Bilibili.Enabled {
		bilibili, err := newBilibiliService(app.configPath, cfg.Bilibili)
		if err != nil {
			log.Printf("📺 B 站音乐未启用: %v", err)
		} else {
			a.bilibili = bilibili
		}
	}

	// 恢复上次的播放队列：服务重启后"下一首"才不至于报"没有正在播放的歌单"
	a.loadMusic(filepath.Join(filepath.Dir(a.configPath), "music-queue.json"))
	if cfg := app.Config(); cfg != nil && cfg.Audiobook.Enabled && a.knowledge != nil {
		books, err := newAudiobookService(app.configPath, cfg.Audiobook, a.knowledge, &a.music.opMu)
		if err != nil {
			log.Printf("⚠️ [听书] 初始化失败：%v", err)
		} else {
			a.audiobooks = books
			books.onSelect = func() { a.cancelBilibili(true, ""); a.persistMusic() }
			books.ttsConfig = func() TTSConfig { return app.Config().TTS }
			books.baseURL = app.ttsBaseURL
			books.busy = func() bool {
				return app.speaker.Speaking() || app.speaker.TakeoverActive() || app.speaker.NativeASRWindow()
			}
			books.play = func(url string, duration int) error {
				app.speaker.ClearPendingMusic()
				if err := app.speaker.PlayMusicURL(url); err != nil {
					return err
				}
				a.cancelBilibili(true, "")
				a.persistMusic()
				app.speaker.setMusicDuration(duration)
				return nil
			}
			books.pause, books.resume, books.stop = app.speaker.PauseMusic, app.speaker.ResumeMusic, app.speaker.StopMusic
			books.mediaURL = func() string { return app.speaker.stateSnapshot().musicURL }
			books.mediaPlaying = app.speaker.MusicPlaying
		}
	}

	// music-queue.json 里还有 currentURL；补回 Speaker，原生 dialog 停掉音乐后
	// EndNativeASRWindow 才能用它重新拉起当前曲目。
	a.music.mu.Lock()
	restoredMusicURL := a.music.currentURL
	restoredDuration := 0
	if a.music.current != nil {
		restoredDuration = a.music.current.Duration
	}
	if q := a.music.bilibili; q != nil {
		restoredMusicURL = q.URL
		restoredDuration = q.Items[q.Index].Duration * 1000
	}
	a.music.mu.Unlock()
	if restoredMusicURL != "" {
		a.speaker.SetMusicURL(restoredMusicURL)
		a.speaker.setMusicDuration(restoredDuration)
		log.Printf("🎵 已恢复当前曲目 URL，供音乐中断后重拉")
	}
	if a.audiobooks != nil && a.audiobooks.initialURL != "" {
		a.speaker.SetMusicURL(a.audiobooks.initialURL)
	}

	// 一首自然播完 → 自动接当前队列的下一首；队列到头才退出音乐态。
	// 没有这个回调，设备播完一首就停，用户还得再喊一次"下一首"。
	if app.speaker != nil {
		app.speaker.SetMusicEndedHandler(func(generation uint64) bool {
			a.music.opMu.Lock()
			defer a.music.opMu.Unlock()
			if !app.speaker.musicGenerationActive(generation) {
				return false
			}
			if handled, advanced := a.audiobooks.endedLocked(); handled {
				return advanced
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			msg, err := a.nextMusicLocked(ctx)
			if err != nil {
				a.music.mu.Lock()
				failed := a.music.lastError != ""
				if q := a.music.bilibili; q != nil {
					failed = q.Error != ""
				}
				a.music.mu.Unlock()
				if failed {
					app.speaker.setMusicError("自动切歌失败，请重试或选择其他歌曲")
				}
				log.Printf("🎵 队列没有下一首了: %v", err)
				return false
			}
			log.Printf("🎵 自动续播: %s", msg)
			return true
		})
	}

	// 音乐态歌词过滤（L3）：按歌曲 id 缓存歌词，判断一句话是不是正在播的歌词
	a.lyrics = newLyricStore(func(ctx context.Context, id int64) (string, error) {
		return a.neteaseClient().Lyric(ctx, id)
	})
	// 音乐态语义路由（L4）：白名单/唤醒词/歌词都没拦住的句子才交给模型
	if app.engine != nil {
		app.engine.SetMusicRouting(a.musicDesc, a.searchAndPlay)
		// 路由直接调用我们自己的 MCP 工具（不再需要为每个能力写分发分支）
		tools := openAIToolsFromMCP(mcpToolList())
		app.engine.SetToolAccess(func() []openai.Tool { return tools }, func(ctx context.Context, name string, args map[string]any) (string, error) {
			return a.runMCPTool(ctx, mcpToolCall{Name: name, Arguments: args})
		})
	}
	return a
}

func (a *adminServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleGetConfig(w, r)
	case http.MethodPut:
		a.handleSaveConfig(w, r)
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
	}
}

func (a *adminServer) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile(a.configPath)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("读取配置失败：%v", err))
		return
	}
	writeAdminJSON(w, http.StatusOK, adminConfigResponse{
		Path:    a.configPath,
		Content: string(data),
		Message: "配置已读取",
	})
}

func (a *adminServer) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, adminMaxRequestBytes)
	var req adminConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeAdminError(w, http.StatusBadRequest, "配置内容不能为空")
		return
	}

	cfg, err := loadConfigFromBytes([]byte(req.Content))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, fmt.Sprintf("配置 YAML 解析失败：%v", err))
		return
	}

	result := adminApplyResult{RestartRequired: true}
	if a.applyConfig != nil {
		result, err = a.applyConfig(cfg)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("配置未保存，热加载失败：%v", err))
			return
		}
	}
	if err := writeFileAtomic(a.configPath, []byte(req.Content)); err != nil {
		writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("保存配置失败：%v", err))
		return
	}
	message := "配置已保存"
	if result.MusicReloaded {
		message += "，music 已热加载"
	}
	if result.MusicStopped {
		message += "，music 已停止"
	}
	if result.RestartRequired {
		message += "；部分 chat 配置需要重启服务后完全生效"
	}
	writeAdminJSON(w, http.StatusOK, adminConfigResponse{
		Path:            a.configPath,
		Message:         message,
		MusicReloaded:   result.MusicReloaded,
		MusicStopped:    result.MusicStopped,
		RestartRequired: result.RestartRequired,
	})
}

func (a *adminServer) handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, adminMaxRequestBytes)
	var req adminTTSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeAdminError(w, http.StatusBadRequest, "TTS 文本不能为空")
		return
	}
	if a.playTTS == nil {
		writeAdminError(w, http.StatusInternalServerError, "TTS 播放器未初始化")
		return
	}
	if err := a.playTTS(text); err != nil {
		writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("发送到音箱失败：%v", err))
		return
	}
	writeAdminJSON(w, http.StatusOK, adminResponse{Message: "已发送到音箱播放"})
}

func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAdminError(w http.ResponseWriter, status int, message string) {
	writeAdminJSON(w, status, adminErrorResponse{Error: message})
}
