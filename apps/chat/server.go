package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/cxjava/open-xiaoai/apps/client/base"
	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
	"github.com/cxjava/open-xiaoai/apps/client/utils"
)

func parseBasicAuth(r *http.Request) (username, password string, ok bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		return "", "", false
	}
	payload, err := base64.StdEncoding.DecodeString(auth[6:])
	if err != nil {
		return "", "", false
	}
	pair := strings.SplitN(string(payload), ":", 2)
	if len(pair) != 2 {
		return "", "", false
	}
	return pair[0], pair[1], true
}

func startServer(ctx context.Context, app *appRuntime) error {
	cfg := app.Config()
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	log.Printf("✅ 已启动: %s", addr)
	// 端口已经在 listen，codex 启动时才能连上 /mcp
	app.StartCodex(ctx)
	app.StartTasks(ctx)

	// 服务端唤醒：VAD 切出的一句话 → ASR（可切换豆包/本地）→ 唤醒词/对话路由
	if cfg.ASR.Enabled {
		var router *asrRouter // 在下面创建，先声明给闭包引用
		routeASRText := func(text string, pcm []byte, wav string) {
			// 指示灯不在这里点：要等引擎判断"这句确实要交给路由/主 agent"才亮，
			// 否则房间底噪、歌词碎片识别出来也会点亮，灯就常亮了。
			// 唤醒词可能被听错：给它一个「用备用模型重认一次」的机会
			alt := func() string {
				if wav == "" {
					return ""
				}
				actx, acancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer acancel()
				t, err := router.TranscribeAlt(actx, wav)
				if err != nil {
					log.Printf("⚠️ 备用模型识别失败: %v", err)
					return ""
				}
				return t
			}
			// 唤醒之后的识别：按 asr.dialog_provider 选定的引擎再认一遍
			dialog := func(current string) string {
				if wav == "" {
					return current
				}
				dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer dcancel()
				t, err := router.TranscribeDialog(dctx, current, pcm, wav)
				if err != nil {
					log.Printf("⚠️ 对话识别失败（%v），沿用 %q", err, current)
					return current
				}
				return t
			}
			app.engine.OnPCMUtteranceWithASR(text, UtteranceASR{Alt: alt, Dialog: dialog})
		}
		skipIfSpeaking := func() bool {
			// 我们自己正在播报：麦克风会录到自己的声音
			if app.speaker.Speaking() {
				log.Printf("🔇 豆包播报中，忽略本段音频")
				return true
			}
			// 小爱正在播报：同样不该把它的话当成用户输入（否则会喂给豆包）
			if app.engine.XiaoaiSpeaking() {
				log.Printf("🔇 小爱播报中，忽略本段音频")
				return true
			}
			return false
		}

		doubaoASR := NewDoubaoASR(cfg.ASR.APIKey, cfg.ASR.ResourceID)
		streamASR := NewDoubaoStreamASR(cfg.ASR.APIKey, cfg.ASR.StreamResourceID)

		var localASR *ASRWorker
		if strings.TrimSpace(cfg.ASR.Script) != "" {
			localASR = NewASRWorker(cfg.ASR.Python, cfg.ASR.Script, cfg.ASR.ModelDir, cfg.ASR.AltModelDir)
			if err := localASR.Start(); err != nil {
				log.Printf("⚠️ 本地 ASR 启动失败: %v", err)
				localASR = nil
			}
		}

		router = newASRRouter(cfg.ASR.WakeProviderName(), cfg.ASR.DialogProviderName(), doubaoASR, streamASR, localASR)
		app.setASRRouter(router)

		micListener.SetUtteranceHandler(func(pcm []byte, wav string) {
			if skipIfSpeaking() {
				return
			}
			// 注意：这里不点灯——房间底噪也会触发切句，按"开口"点灯会常亮。
			// 灯在识别出有效文本之后才点亮（见 routeASRText）。
			ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			text, err := router.TranscribeWake(ctx2, pcm, wav)
			if err != nil {
				log.Printf("⚠️ ASR 失败: %v", err)
				return
			}
			if strings.TrimSpace(text) != "" {
				routeASRText(text, pcm, wav)
			}
		})
		log.Printf("🧠 服务端唤醒已启用（唤醒: %s，对话: %s，可用: %v）",
			providerLabel(router.WakeProvider()), providerLabel(router.DialogProvider()), router.Status()["available"])
	}

	mux := http.NewServeMux()
	admin := app.Admin()
	mux.HandleFunc("/admin/api/audiobooks", withAppAuth(app, admin.handleAudiobooks))
	mux.HandleFunc("/admin/api/audiobooks/", withAppAuth(app, admin.handleAudiobooks))
	mux.HandleFunc("/audiobooks/audio/", admin.handleAudiobookAudio)
	mux.HandleFunc("/admin/api/config", withAppAuth(app, admin.handleConfig))
	mux.HandleFunc("/admin/api/tts", withAppAuth(app, admin.handleTTS))
	mux.HandleFunc("/admin/api/tts/voices", withAppAuth(app, admin.handleTTSVoices))
	mux.HandleFunc("/admin/api/debug/tts-probe", admin.handleTTSProbe)
	mux.HandleFunc("/admin/api/chat", withAppAuth(app, admin.handleChatTest))
	mux.HandleFunc("/admin/api/tasks", withAppAuth(app, admin.handleTasks))
	mux.HandleFunc("/admin/api/debug/shell", admin.handleDebugShell)
	mux.HandleFunc("/admin/api/debug/mic-capture", admin.handleDebugMicCapture)
	mux.HandleFunc("/admin/api/debug/music-state", admin.handleDebugMusicState)
	mux.HandleFunc("/admin/api/debug/native-asr", admin.handleDebugNativeASR)
	mux.HandleFunc("/admin/api/debug/route", admin.handleDebugRoute)
	mux.HandleFunc("/admin/api/debug/lyric", admin.handleDebugLyric)
	mux.HandleFunc("/admin/api/debug/audio-test", withAppAuth(app, admin.handleAudioTest))
	mux.HandleFunc("/admin/api/debug/utterance", admin.handleDebugUtterance)
	mux.HandleFunc("/admin/api/debug/doubao-chat", admin.handleDebugDoubaoChat)
	mux.HandleFunc("/admin/api/debug/doubao-raw", admin.handleDebugDoubaoRaw)
	mux.HandleFunc("/admin/api/debug/doubao-reset", admin.handleDebugDoubaoReset)
	mux.HandleFunc("/admin/api/ha/devices", withAppAuth(app, admin.handleHADevices))
	mux.HandleFunc("/admin/api/ha/control", withAppAuth(app, admin.handleHAControl))
	mux.HandleFunc("/admin/api/tasks/action", withAppAuth(app, admin.handleTaskAction))
	mux.HandleFunc("/admin/api/doubao/health", withAppAuth(app, admin.handleDoubaoHealth))
	mux.HandleFunc("/admin/api/doubao/login", withAppAuth(app, admin.handleDoubaoLogin))
	mux.HandleFunc("/admin/api/doubao/conversations", withAppAuth(app, admin.handleDoubaoConversations))
	mux.HandleFunc("/admin/api/doubao/messages", withAppAuth(app, admin.handleDoubaoMessages))
	mux.HandleFunc("/admin/api/doubao/jobs", withAppAuth(app, admin.handleDoubaoJobs))
	mux.HandleFunc("/admin/api/doubao/jobs/speak", withAppAuth(app, admin.handleDoubaoSpeak))
	mux.HandleFunc("/admin/api/doubao/jobs/poll", withAppAuth(app, admin.handleDoubaoJobsPoll))
	mux.HandleFunc("/admin/api/doubao/web/status", withAppAuth(app, admin.handleDoubaoWebStatus))
	mux.HandleFunc("/admin/api/doubao/web/qr", withAppAuth(app, admin.handleDoubaoWebQR))
	mux.HandleFunc("/admin/api/asr", withAppAuth(app, admin.handleASR))
	mux.HandleFunc("/admin/api/wake/test", withAppAuth(app, admin.handleWakeTest))
	mux.HandleFunc("/admin/api/wake", withAppAuth(app, admin.handleWakeWords))
	mux.HandleFunc("/admin/api/wake/recent", withAppAuth(app, admin.handleWakeRecent))
	mux.HandleFunc("/admin/api/route/recent", withAppAuth(app, admin.handleRouteRecent))
	mux.HandleFunc("/admin/api/agents", withAppAuth(app, admin.handleAgents))
	mux.HandleFunc("/admin/api/agents/action", withAppAuth(app, admin.handleAgentAction))
	mux.HandleFunc("/admin/api/knowledge", withAppAuth(app, admin.handleKnowledge))
	mux.HandleFunc("/admin/api/knowledge/", withAppAuth(app, admin.handleKnowledgeSub))
	mux.HandleFunc("/admin/api/music", withAppAuth(app, admin.handleMusicPlayer))
	mux.HandleFunc("/admin/api/bilibili/status", withAppAuth(app, admin.handleBilibiliStatus))
	mux.HandleFunc("/admin/api/bilibili/qr", withAppAuth(app, admin.handleBilibiliQR))
	mux.HandleFunc("/admin/api/bilibili/search", withAppAuth(app, admin.handleBilibiliSearch))
	mux.HandleFunc("/admin/api/bilibili/catalog", withAppAuth(app, admin.handleBilibiliCatalog))
	mux.HandleFunc("/admin/api/bilibili/queue", withAppAuth(app, admin.handleBilibiliQueue))
	mux.HandleFunc("/admin/api/bilibili/play", withAppAuth(app, func(w http.ResponseWriter, r *http.Request) {
		admin.handleBilibiliAction(w, r, false)
	}))
	mux.HandleFunc("/admin/api/bilibili/download", withAppAuth(app, func(w http.ResponseWriter, r *http.Request) {
		admin.handleBilibiliAction(w, r, true)
	}))
	mux.HandleFunc("/admin/api/netease/status", withAppAuth(app, admin.handleNeteaseStatus))
	mux.HandleFunc("/admin/api/netease/qr", withAppAuth(app, admin.handleNeteaseQR))
	// MCP 工具端点：只允许本机访问（codex 在同一台机器上）
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
			http.Error(w, "只允许本机访问", http.StatusForbidden)
			return
		}
		admin.handleMCP(w, r)
	})
	// 管理台前端（构建产物放在 config 同级的 web/ 目录即可）：
	//   http://<host>:4399/ui/
	webDir := filepath.Join(filepath.Dir(app.configPath), "web")
	// 开发时构建产物在 web/dist（web/index.html 是 Vite 的开发入口，不能用）；
	// 部署时直接把 dist 内容放到 web/，此时没有 dist 子目录。
	if _, err := os.Stat(filepath.Join(webDir, "dist", "index.html")); err == nil {
		webDir = filepath.Join(webDir, "dist")
	}
	if st, err := os.Stat(webDir); err == nil && st.IsDir() {
		mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir(webDir))))
		log.Printf("🖥️ 管理台: http://%s:%d/ui/", cfg.Server.Host, cfg.Server.Port)
	}
	// 静音音频：给 agent 思考时点亮播放灯用
	mux.HandleFunc("/silence.wav", admin.handleSilence)
	// 豆包 TTS 合成的音频由本服务中转给音箱拉取
	mux.HandleFunc("/tts/", func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		if name == "" || name == "." || name == "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(defaultTTSDir(), name))
	})
	mux.HandleFunc("/bilibili/audio/", admin.handleBilibiliAudio)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAppRequest(app, w, r) {
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			log.Printf("❌ WebSocket accept: %v", err)
			return
		}
		handleConnection(conn, r, app)
	})

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !applyCORS(app, w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	})}
	go func() {
		<-ctx.Done()
		server.Shutdown(context.Background())
	}()

	return server.Serve(listener)
}

func withAppAuth(app *appRuntime, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAppRequest(app, w, r) {
			return
		}
		next(w, r)
	}
}

func authorizeAppRequest(app *appRuntime, w http.ResponseWriter, r *http.Request) bool {
	cfg := app.Config()
	if !cfg.Auth.RequiresAuth() {
		return true
	}
	user, pass, ok := parseBasicAuth(r)
	if !ok || !cfg.Auth.ValidateAuth(user, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="open-xiaoai"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func handleConnection(conn *websocket.Conn, r *http.Request, app *appRuntime) {
	log.Printf("✅ 已连接: %s", r.RemoteAddr)
	if initConnection(conn, r, app) {
		log.Printf("🔁 新连接 %s 顶替了已有设备连接（当前仅支持单设备，旧连接已关闭）", r.RemoteAddr)
	}

	err := connect.GetMessageManager().ProcessMessages()
	if err != nil && connect.GetMessageManager().IsCurrent(conn) {
		log.Printf("❌ 消息处理异常: %v", err)
	}

	if disposeConnection(conn) {
		log.Printf("❌ 已断开连接: %s", r.RemoteAddr)
	} else {
		log.Printf("❌ 旧连接已被顶替并关闭: %s（不影响当前连接）", r.RemoteAddr)
	}
}

// restorePendingMusicDuck 设备连上后，把上次进程被 kill 时没来得及还原的音量补回来。
// 必须异步：这里调 CallRemote 要等 ProcessMessages 收到响应，同步调用会自己等自己。
func restorePendingMusicDuck(app *appRuntime) {
	go func() {
		time.Sleep(time.Second)
		app.speaker.RestorePendingMusicDuck()
		// 服务重启期间设备可能还在放歌：把内存状态对齐到真实播放器，
		// 否则后面会把"放歌中"当成"没在放"，音乐就再也接不回去。
		app.speaker.SyncMusicStateFromDevice()
		// 让客户端在原生 ASR 终态时本地预冻结 aivs，抢在 TTS 请求之前。
		if cfg := app.Config(); cfg != nil {
			app.speaker.SyncNativeGuard(cfg)
		}
	}()
}

func initConnection(conn *websocket.Conn, r *http.Request, app *appRuntime) (replaced bool) {
	replaced = connect.GetMessageManager().Init(conn)

	// 连接感知 base_url：客户端用哪个 host 连上来，就用同一 host 拼音乐 URL（支持 LAN/Tailscale）
	if r.Host != "" {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "" {
			app.OnConnectionHost(host)
		}
	}

	connect.GetHandlers().SetEventHandler(func(event connect.Event) error {
		// 事件分发：music 模块和 chat engine 都要看到事件，原因：
		//
		// 1) 即使 music 处理了某条 instruction（如 "闭嘴" 命中 stop_keywords 停了音乐），
		//    chat engine 仍然必须收到这条 instruction，否则正在进行的 AI 流回复（LLM 流式输出 + TTS 队列）
		//    不会被取消。用户说"闭嘴"的真实意图是**停掉所有声音**，不只是音乐。
		//
		// 2) 同样地，"播放周杰伦"这类 music 命令进入 engine 后，因为不含 call_ai/interrupt 关键词，
		//    会走 instructionDecisionIgnore 分支安全跳过，不会重复触发 AI。
		//
		// 3) playing / kws 等非 instruction 事件，两者也都需要：music 用 playing 同步播放状态，
		//    engine 用 playing 更新 speaker.UpdateStatus，kws 用于打断 AI 流。
		//
		// 历史 bug：之前这里有 `if musicModule.OnEvent(event) { return }` 截胡，导致 "闭嘴" 停了音乐但
		// AI 还在源源不断输出。现已改为两者都分发。
		if musicModule := app.MusicModule(); musicModule != nil {
			musicModule.OnEvent(event)
		}
		app.engine.OnEvent(event)
		return nil
	})
	connect.GetHandlers().SetStreamHandler(func(stream connect.Stream) error {
		switch stream.Tag {
		case "record":
			log.Printf("🎤 收到录音: %d bytes", len(stream.Bytes))
		case "mic":
			// 服务端唤醒模式：音箱持续推上来的麦克风 PCM（16k/mono/S16_LE）
			if app.engine != nil && app.engine.LocalASRDisabled() {
				return nil
			}
			micListener.Feed(stream.Bytes)
		}
		return nil
	})

	connect.GetRPC().AddCommand("get_version", func(_ connect.Request) (connect.Response, error) {
		data, _ := json.Marshal(base.VERSION)
		raw := json.RawMessage(data)
		return connect.Response{ID: "0", Data: &raw}, nil
	})

	// 连接后补还原上次没还原的音乐音量（部署/崩溃时停在 duck 值上）
	restorePendingMusicDuck(app)

	// 连接后播放欢迎语。底层 TTS 已通过 client-go 的 ducking 事件避免打断背景音乐；
	// 配置 greeting: "" 可以彻底关掉欢迎语（不再回退到 "已连接"）。
	go func() {
		time.Sleep(2 * time.Second)
		greeting := app.Config().Greeting
		if greeting == "" {
			log.Printf("🔕 跳过欢迎语：greeting 为空")
			return
		}
		log.Printf("👋 播放欢迎语: %q", greeting)
		if err := app.speaker.PlayTTS(greeting, false); err != nil {
			log.Printf("⚠️ 欢迎语播放失败: %v", err)
		}
	}()
	return replaced
}

// disposeConnection 只清理 conn 自己；若已被新连接顶替则什么都不动，返回 false。
func disposeConnection(conn *websocket.Conn) bool {
	if !connect.GetMessageManager().DisposeConn(conn) {
		return false
	}
	utils.GetTaskManager().Dispose("test")
	return true
}

// applyCORS 处理跨域。返回 false 表示请求已被终结（预检请求）。
func applyCORS(app *appRuntime, w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	cfg := app.Config()
	allowed, allowOrigin := false, ""
	if cfg == nil || len(cfg.CORS.AllowedOrigins) == 0 {
		allowed, allowOrigin = true, "*"
	} else {
		for _, o := range cfg.CORS.AllowedOrigins {
			if o == "*" || strings.EqualFold(o, origin) {
				allowed, allowOrigin = true, origin
				break
			}
		}
	}
	if allowed {
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}
