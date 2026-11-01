package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"github.com/cxjava/open-xiaoai/pkg/music"
)

type appRuntime struct {
	mu                 sync.RWMutex
	reloadMu           sync.Mutex
	configPath         string
	config             *AppConfig
	engine             *Engine
	speaker            *Speaker
	musicModule        *music.Module
	lastConnectionHost string
	admin              *adminServer
	scheduleWatcher    *ScheduleWatcher
	doubaoWeb          *DoubaoWeb
	asrRouter          *asrRouter
	wakeStore          *wakeStore
	neteaseStore       *neteaseStore
	codexServer        *CodexAppServer
	taskStore          *taskStore
	taskRunner         TaskRunner
	taskScheduler      *TaskScheduler
}

func newAppRuntime(configPath string, cfg *AppConfig, speaker *Speaker, engine *Engine) *appRuntime {
	a := &appRuntime{
		configPath: configPath,
		config:     cfg,
		speaker:    speaker,
		engine:     engine,
	}
	if cfg != nil && cfg.Chat.UseCodex() && a.engine != nil {
		cc := cfg.Codex
		if cc.BaseInstructions == "" {
			cc.BaseInstructions = systemPromptWithEmotion(cfg.Prompt.System)
		}
		srv := NewCodexAppServer(cc)
		srv.SetThreadFile(filepath.Join(filepath.Dir(a.configPath), "codex-thread.json"))
		a.codexServer = srv
		a.engine.SetCodexAppServer(srv)
		a.engine.SetCodexClient(NewCodexClient(cc))
	}
	a.wakeStore = newWakeStore(configPath)
	a.neteaseStore = newNeteaseStore(configPath)
	// 本机定时任务：独立于豆包 job_cron，任务到点由服务端执行并播报
	loc := loadTZ("")
	if cfg != nil {
		loc = loadTZ(cfg.Prompt.Timezone)
	}
	a.taskStore = newTaskStore(configPath, loc)
	codexCfg := CodexConfig{}
	if cfg != nil {
		codexCfg = cfg.Codex
	}
	// 有常驻 app-server 就用它的独立 thread（快）；否则回落 codex exec
	var runner TaskRunner
	if a.codexServer != nil {
		runner = NewCodexThreadRunner(a.codexServer)
	} else {
		runner = NewCodexClient(codexCfg)
	}
	a.taskRunner = runner
	a.taskScheduler = NewTaskScheduler(a.taskStore, speaker, runner, cfgSchedules(cfg), loc)
	if a.engine != nil {
		a.engine.SetWakeStore(a.wakeStore)
	}
	if cfg != nil && cfg.DoubaoWeb.Enabled {
		a.doubaoWeb = NewDoubaoWeb(cfg.DoubaoWeb.SessionPath())
		if a.engine != nil {
			a.engine.SetDoubaoWeb(a.doubaoWeb)
		}
	}
	a.admin = newAdminServer(a)
	if a.engine != nil {
		a.engine.SetMusicControls(
			func(ctx context.Context) (string, error) { return a.admin.nextMusic(ctx) },
			func() { a.admin.stopPlayback() },
		)
	}
	a.admin.tasks = a.taskStore
	if a.taskScheduler != nil {
		a.admin.runTask = func(id string) error {
			return a.taskScheduler.RunNow(context.Background(), id)
		}
		a.admin.stopTask = a.taskScheduler.StopTask
	}
	return a
}

// StartCodex 在 HTTP 监听建立之后再拉起常驻 codex：
// codex 启动时会连 /mcp，端口必须已经在 listen，否则工具会缺失。
func (a *appRuntime) StartCodex(ctx context.Context) {
	if a == nil || a.codexServer == nil {
		return
	}
	go func() {
		if err := a.codexServer.Start(ctx); err != nil {
			log.Printf("⚠️ codex app-server 启动失败（会回落到 exec 模式）: %v", err)
		}
	}()
}

func cfgSchedules(cfg *AppConfig) ScheduleConfig {
	if cfg == nil {
		return ScheduleConfig{}
	}
	return cfg.Schedules
}

// StartTasks 启动定时任务扫描（HTTP 已在监听后调用）。
func (a *appRuntime) StartTasks(ctx context.Context) {
	if a == nil || a.taskScheduler == nil {
		return
	}
	a.taskScheduler.Start(ctx)
	log.Printf("⏰ 本机定时任务: 已启动（%d 个）", len(a.taskStore.List()))
}

func (a *appRuntime) Admin() *adminServer {
	return a.admin
}

// NeteaseStore 返回网易云登录态。
func (a *appRuntime) NeteaseStore() *neteaseStore { return a.neteaseStore }

// WakeStore 返回用户自定义唤醒词表。
func (a *appRuntime) WakeStore() *wakeStore { return a.wakeStore }

// ASRRouter 返回语音识别路由；未启用服务端唤醒时为 nil。
func (a *appRuntime) ASRRouter() *asrRouter {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.asrRouter
}

func (a *appRuntime) setASRRouter(r *asrRouter) {
	a.mu.Lock()
	a.asrRouter = r
	a.mu.Unlock()
}

// DoubaoWeb 返回豆包网页直连客户端；未启用时为 nil。
func (a *appRuntime) DoubaoWeb() *DoubaoWeb {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.doubaoWeb
}

func (a *appRuntime) ScheduleWatcher() *ScheduleWatcher {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.scheduleWatcher
}

func (a *appRuntime) setScheduleWatcher(w *ScheduleWatcher) {
	a.mu.Lock()
	a.scheduleWatcher = w
	a.mu.Unlock()
}

func (a *appRuntime) Config() *AppConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config
}

func (a *appRuntime) MusicModule() *music.Module {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.musicModule
}

func (a *appRuntime) OnConnectionHost(host string) {
	if host == "" {
		return
	}
	a.mu.Lock()
	a.lastConnectionHost = host
	module := a.musicModule
	a.mu.Unlock()
	if module != nil {
		module.SetBaseURLForConnection(host)
	}
}

// ttsBaseURL 返回音箱可访问的本服务地址：用客户端连上来的 host + 监听端口，
// 这样 LAN / Tailscale 场景下音箱都能取到 TTS 音频。
func (a *appRuntime) ttsBaseURL() string {
	a.mu.RLock()
	host := a.lastConnectionHost
	cfg := a.config
	a.mu.RUnlock()
	if host == "" || cfg == nil {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
}

// ApplyTTS 按配置启用/关闭豆包语音合成（provider=xiaoai 时回退到音箱自带 TTS）。
func (a *appRuntime) ApplyTTS(cfg *AppConfig) {
	if cfg != nil && cfg.TTS.UsesDoubao() {
		a.speaker.SetDoubaoTTS(NewDoubaoTTS(cfg.TTS), a.ttsBaseURL)
		return
	}
	a.speaker.SetDoubaoTTS(nil, nil)
}

// ApplyMusicDuck 按配置设置放音乐时的自动降音量。
func (a *appRuntime) ApplyMusicDuck(cfg *AppConfig) {
	if cfg == nil {
		return
	}
	a.speaker.SetMusicDuck(cfg.MusicDuck.Enabled, cfg.MusicDuck.Volume)
}

func (a *appRuntime) StartInitialMusic(ctx context.Context) error {
	cfg := a.Config()
	if !cfg.Music.Enabled {
		return nil
	}
	module := music.New(&cfg.Music)
	if err := module.Start(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	a.musicModule = module
	a.mu.Unlock()
	return nil
}

func (a *appRuntime) StopMusic() {
	a.mu.Lock()
	module := a.musicModule
	a.musicModule = nil
	a.mu.Unlock()
	if module != nil {
		_ = module.Stop()
	}
}

func (a *appRuntime) ApplyConfig(cfg *AppConfig) (adminApplyResult, error) {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	a.mu.Lock()
	oldConfig := a.config
	oldMusic := a.musicModule
	host := a.lastConnectionHost
	a.musicModule = nil
	a.mu.Unlock()

	if oldMusic != nil {
		_ = oldMusic.Stop()
	}

	var newMusic *music.Module
	if cfg.Music.Enabled {
		newMusic = music.New(&cfg.Music)
		if host != "" {
			newMusic.SetBaseURLForConnection(host)
		}
		if err := newMusic.Start(context.Background()); err != nil {
			rollbackMusic := startMusicBestEffort(oldConfig, host)
			a.mu.Lock()
			a.config = oldConfig
			a.musicModule = rollbackMusic
			a.engine.UpdateConfig(oldConfig)
			a.mu.Unlock()
			return adminApplyResult{MusicReloaded: false, RestartRequired: configRequiresRestart(oldConfig, cfg)}, err
		}
	}

	a.mu.Lock()
	a.config = cfg
	a.musicModule = newMusic
	a.engine.UpdateConfig(cfg)
	asr := a.asrRouter
	a.mu.Unlock()

	// 唤醒/对话识别引擎支持热切换：改 config.yaml 保存后立刻生效
	if asr != nil {
		if p := cfg.ASR.WakeProviderName(); p != "" {
			if err := asr.SetWakeProvider(p); err != nil {
				log.Printf("⚠️ 唤醒识别切换到 %q 失败: %v", p, err)
			}
		}
		if p := cfg.ASR.DialogProviderName(); p != "" {
			if err := asr.SetDialogProvider(p); err != nil {
				log.Printf("⚠️ 对话识别切换到 %q 失败: %v", p, err)
			}
		}
	}

	a.ApplyTTS(cfg)
	a.ApplyMusicDuck(cfg)

	return adminApplyResult{
		MusicReloaded:   cfg.Music.Enabled,
		MusicStopped:    oldMusic != nil && !cfg.Music.Enabled,
		RestartRequired: configRequiresRestart(oldConfig, cfg),
	}, nil
}

func startMusicBestEffort(cfg *AppConfig, host string) *music.Module {
	if cfg == nil || !cfg.Music.Enabled {
		return nil
	}
	module := music.New(&cfg.Music)
	if host != "" {
		module.SetBaseURLForConnection(host)
	}
	if err := module.Start(context.Background()); err != nil {
		return nil
	}
	return module
}

func configRequiresRestart(oldConfig, newConfig *AppConfig) bool {
	if oldConfig == nil || newConfig == nil {
		return false
	}
	return oldConfig.Server != newConfig.Server ||
		oldConfig.Proxy != newConfig.Proxy ||
		oldConfig.LLM != newConfig.LLM
}
