package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"
	"time"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	resolvedConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatalf("❌ 解析配置路径失败: %v", err)
	}

	cfg, err := loadConfig(resolvedConfigPath)
	if err != nil {
		log.Fatalf("❌ 加载配置失败: %v", err)
	}

	speaker := NewSpeaker()
	speaker.SetMusicDuckFile(filepath.Join(filepath.Dir(resolvedConfigPath), "music-duck.json"))
	engine := NewEngine(cfg, speaker)
	app := newAppRuntime(resolvedConfigPath, cfg, speaker, engine)
	app.ApplyTTS(cfg)
	app.ApplyMusicDuck(cfg)

	ctx := context.Background()
	if err := app.StartInitialMusic(ctx); err != nil {
		log.Fatalf("❌ 音乐模块启动失败: %v", err)
	}
	defer app.StopMusic()

	log.Println("✅ Chat-Go 已启动")
	log.Printf("   监听: %s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("   模型: %s", cfg.GetLLM().Model)
	log.Printf("   关键词: %v", cfg.CallAIKeywords)
	if cfg.TTS.UsesDoubao() {
		log.Printf("   语音: 豆包 TTS (speaker=%s, format=%s)", cfg.TTS.Speaker, cfg.TTS.Format)
	} else {
		log.Printf("   语音: 音箱自带 TTS")
	}
	log.Printf("   打断: keywords=%v match=%s kws=%v", cfg.Interrupt.Keywords, cfg.Interrupt.MatchMode, cfg.Interrupt.KwsInterrupt)
	if cfg.Music.Enabled {
		log.Printf("   音乐: 已启用")
	}
	if cfg.Schedules.Enabled {
		if !cfg.DoubaoWeb.Enabled {
			log.Printf("   定时任务: 已跳过（未启用 doubao_web）")
		} else {
			loc, err := time.LoadLocation(cfg.Prompt.Timezone)
			if err != nil {
				loc = time.Local
			}
			statePath := filepath.Join(filepath.Dir(resolvedConfigPath), "schedule-state.json")
			watcher := NewScheduleWatcher(app.Admin(), app.DoubaoWeb(), speaker, cfg.Schedules, statePath, loc)
			app.setScheduleWatcher(watcher)
			watcher.Start(ctx)
			defer watcher.Stop()
			quiet := cfg.Schedules.QuietHours
			if quiet == "" {
				quiet = "不静默"
			}
			log.Printf("   定时任务: 每 %s 轮询豆包任务，静默时段 %s", watcher.Interval(), quiet)
		}
	}

	log.Printf("   管理 API: http://%s:%d/admin/api/", cfg.Server.Host, cfg.Server.Port)
	log.Printf("   前端页面: apps/chat/web/index.html（可单独部署，填入 API 地址即可）")

	if err := startServer(ctx, app); err != nil {
		log.Fatalf("❌ server error: %v", err)
	}
}
