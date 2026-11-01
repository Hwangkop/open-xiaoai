package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"
	"time"

	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
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

	go clearStaleNativeFreeze()

	if err := startServer(ctx, app); err != nil {
		log.Fatalf("❌ server error: %v", err)
	}
}

// clearStaleNativeFreeze 启动自检：上一次进程可能在接管途中退出，
// 设备播放器还停在 SIGSTOP。等客户端连上后解冻一次（对正常播放是无害的 SIGCONT）。
func clearStaleNativeFreeze() {
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		if _, err := connect.GetRPC().CallRemote("unfreeze_native", map[string]int{"pid": 0}, &nativeTakeoverRPCTimeoutMs); err == nil {
			log.Printf("🧹 启动自检：已清除可能残留的原生播放冻结")
			return
		}
	}
}
