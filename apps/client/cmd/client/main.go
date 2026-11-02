package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/cxjava/open-xiaoai/apps/client/base"
	"github.com/cxjava/open-xiaoai/apps/client/services/audio"
	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
	"github.com/cxjava/open-xiaoai/apps/client/services/monitor"
	"github.com/cxjava/open-xiaoai/apps/client/services/speechhost"
	"github.com/cxjava/open-xiaoai/apps/client/utils"
)

var (
	flagSwitch         = flag.Bool("switch", false, "切换模式：多地址用于在两套 Server 之间语音切换，说切换词即切换 Server")
	flagSwitchKeywords = flag.String("switch-keywords", "小智模式,对话模式", "切换模式下的触发词，逗号分隔")
	flagCaptureBackend = flag.String("capture-backend", "noop", "音频采集后端：noop（默认）或 usock（接管固件 speech.usock）")
	flagMicStream      = flag.Bool("mic-stream", false, "把麦克风音频持续推给服务端（服务端唤醒模式，配合 -capture-backend noop）")
	flagSpeechHost     = flag.Bool("speech-host", false, "接管 speech.usock（原生助手不再处理语音，配合 init.sh 的 speech_host 模式）")
	flagSpeechSocket   = flag.String("speech-socket", "/tmp/mico_aivs_lab/usock/speech.usock", "语音 socket 路径")
	flagMicDevice      = flag.String("mic-device", "noop", "arecord 采集设备名（固件 dsnoop 共享设备：noop / Capture）")
)

const nativeAssistantName = "mico_aivs_lab"

var (
	// nativeGuardEnabled 由服务端同步：开启后，客户端一看到原生 ASR 的 final
	// 就先本地 SIGSTOP 掉 mico_aivs_lab，抢在它发出 TTS 请求之前。
	nativeGuardEnabled atomic.Bool
	// nativeGuardGen/Claimed 用于兜底：预冻结后 30 秒内服务端没有确认接管，
	// 主动解冻，避免服务端崩溃时把原生语音永久冻住。
	nativeGuardGen     atomic.Int64
	nativeGuardClaimed atomic.Bool
)

func basicAuthHeader(username, password string) string {
	credentials := username + ":" + password
	encoded := base64.StdEncoding.EncodeToString([]byte(credentials))
	return "Basic " + encoded
}

type AppClient struct {
	monitorMu          sync.Mutex
	kwsMonitor         *monitor.KwsMonitor
	instructionMonitor *monitor.InstructionMonitor
	playingMonitor     *monitor.PlayingMonitor
}

// runConfig 运行配置，用于切换模式
type runConfig struct {
	switchMode      bool
	switchKeywords  []string
	switchRequested chan struct{}
}

func NewAppClient() *AppClient {
	return &AppClient{
		kwsMonitor:         monitor.NewKwsMonitor(),
		instructionMonitor: monitor.NewInstructionMonitor(),
		playingMonitor:     monitor.NewPlayingMonitor(),
	}
}

func (c *AppClient) connectWS(ctx context.Context, serverURL string, username, password string) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{}
	if username != "" && password != "" {
		opts.HTTPHeader = http.Header{
			"Authorization": []string{basicAuthHeader(username, password)},
		}
	}
	conn, _, err := websocket.Dial(ctx, serverURL, opts)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	return conn, nil
}

func (c *AppClient) init(conn *websocket.Conn, cfg *runConfig) {
	mgr := connect.GetMessageManager()
	mgr.Init(conn)
	go refreshInstructionGuard(conn)

	connect.GetHandlers().SetEventHandler(onEvent)
	connect.GetHandlers().SetStreamHandler(onStream)

	rpc := connect.GetRPC()
	rpc.AddCommand("get_version", getVersion)
	rpc.AddCommand("run_shell", runShell)
	rpc.AddCommand("stop_tts", stopTTS)
	rpc.AddCommand("stop_native", stopNative)
	rpc.AddCommand("freeze_native", freezeNative)
	rpc.AddCommand("unfreeze_native", unfreezeNative)
	rpc.AddCommand("native_playback_state", nativePlaybackState)
	rpc.AddCommand("native_guard", nativeGuard)
	rpc.AddCommand("native_guard_release", nativeGuardRelease)
	rpc.AddCommand("start_play", startPlay)
	rpc.AddCommand("stop_play", stopPlay)
	rpc.AddCommand("start_recording", c.startRecording)
	rpc.AddCommand("stop_recording", stopRecording)
	rpc.AddCommand("continue_dialog", continueDialog)
	rpc.AddCommand("native_continue_dialog", nativeContinueDialog)
	rpc.AddCommand("drain_native_player", drainNativePlayer)
	rpc.AddCommand("finish_native_dialog", finishNativeDialog)
	rpc.AddCommand("finish_dialog", finishDialog)

	c.instructionMonitor.Start(func(event monitor.FileMonitorEvent) {
		if cfg != nil && cfg.switchMode && event.Type == "NewLine" && event.Line != "" {
			text := parseInstructionText(event.Line)
			for _, kw := range cfg.switchKeywords {
				if strings.Contains(text, strings.TrimSpace(kw)) {
					log.Printf("🔄 检测到切换词 %q，即将切换 Server", text)
					select {
					case cfg.switchRequested <- struct{}{}:
					default:
						// 已有一个待处理的切换请求
					}
					return // 不转发给 Server
				}
			}
		}
		if event.Type == "NewLine" {
			event.InstructionGuarded = instructionGuardClaimed(event.Line)
		}
		// 原生 ASR 终态：先确认 SDK 认领，否则走冻结兼容路径。
		// 原生 TTS 的请求往往在 final 之后几十毫秒内发出，走服务端 RPC 再回来就晚了；
		// 这里用 inotify 叫醒（见 monitor.newFileWake）+ 本地 kill，把窗口压到毫秒级。
		if event.Type == "NewLine" && nativeGuardEnabled.Load() {
			if text := parseInstructionText(event.Line); text != "" {
				if event.InstructionGuarded {
					event.InstructionGuarded = true
					mgr.SendEvent("instruction", event)
					return
				}
				if pid, err := utils.FreezeProcess(nativeAssistantName); err != nil {
					log.Printf("⚠️ [native-guard] 终态识别预冻结失败: %v", err)
				} else {
					event.PreFrozen = true
					log.Printf("❄️ [native-guard] 终态识别 → 本地预冻结 %s(pid=%d) text=%q", nativeAssistantName, pid, text)
					go reportNativeProcesses()
					startNativeProbe(pid)
					armNativeGuardSafety()
				}
			}
		}
		mgr.SendEvent("instruction", event)
	})

	c.playingMonitor.Start(func(status monitor.PlayingStatus) {
		mgr.SendEvent("playing", status)
	})

	startKwsMonitor := func() {
		c.monitorMu.Lock()
		defer c.monitorMu.Unlock()
		c.kwsMonitor.Start(func(event monitor.KwsMonitorEvent) {
			mgr.SendEvent("kws", event)
		})
	}
	if audio.GetRecorder().DefaultBackend() == "usock" {
		if err := c.enableUsock(); err != nil {
			log.Printf("⚠️ speech.usock 接管失败，回滚并继续使用 noop: %v", err)
			startKwsMonitor()
		} else {
			log.Printf("✅ speech.usock 已注册；本次运行不轮询 kws.log")
		}
	} else {
		startKwsMonitor()
	}
}

func (c *AppClient) enableUsock() error {
	mgr := connect.GetMessageManager()
	if err := audio.GetRecorder().EnableUsock(func(event string, data interface{}) {
		if sendErr := mgr.SendEvent(event, data); sendErr != nil {
			log.Printf("⚠️ 发送 speech.usock 事件 %s 失败: %v", event, sendErr)
		}
	}, func() {
		c.monitorMu.Lock()
		defer c.monitorMu.Unlock()
		log.Printf("⚠️ speech.usock 异常退出，已恢复原生语音服务；启用 kws.log 兼容轮询")
		c.kwsMonitor.Start(func(event monitor.KwsMonitorEvent) {
			mgr.SendEvent("kws", event)
		})
	}); err != nil {
		return err
	}
	c.monitorMu.Lock()
	c.kwsMonitor.Stop()
	c.monitorMu.Unlock()
	return nil
}

func (c *AppClient) dispose() {
	disableInstructionGuard()
	connect.GetMessageManager().Dispose()
	audio.GetPlayer().Stop()
	audio.GetRecorder().StopRecording()
	audio.GetRecorder().DisableUsock()
	c.monitorMu.Lock()
	c.instructionMonitor.Stop()
	c.playingMonitor.Stop()
	c.kwsMonitor.Stop()
	c.monitorMu.Unlock()
}

// parseInstructionText 从 instruction.log 行中提取用户最终语音文本
// Line 格式: {"header":{"namespace":"SpeechRecognizer","name":"RecognizeResult"},"payload":{"is_final":true,"results":[{"text":"..."}]}}
func parseInstructionText(line string) string {
	if line == "" {
		return ""
	}
	var msg struct {
		Header  struct{ Namespace, Name string } `json:"header"`
		Payload struct {
			IsFinal bool `json:"is_final"`
			Results []struct {
				Text string `json:"text"`
			} `json:"results"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(strings.NewReader(line)).Decode(&msg); err != nil {
		return ""
	}
	if !strings.EqualFold(msg.Header.Namespace, "SpeechRecognizer") ||
		!strings.EqualFold(msg.Header.Name, "RecognizeResult") {
		return ""
	}
	if !msg.Payload.IsFinal || len(msg.Payload.Results) == 0 {
		return ""
	}
	return strings.TrimSpace(msg.Payload.Results[0].Text)
}

// nativeGuard 服务端同步"终端识别时是否本地预冻结原生助手"。
func nativeGuard(req connect.Request) (connect.Response, error) {
	var p struct {
		Enabled  bool     `json:"enabled"`
		Mode     string   `json:"mode"`
		Keywords []string `json:"native_keywords"`
	}
	if req.Payload == nil {
		return connect.ErrorResponse(req.ID, fmt.Errorf("缺少原生隔离配置")), nil
	}
	if err := json.Unmarshal(*req.Payload, &p); err != nil {
		return connect.ErrorResponse(req.ID, err), nil
	}
	if p.Mode != "" && p.Mode != "freeze" && p.Mode != "instruction" {
		return connect.ErrorResponse(req.ID, fmt.Errorf("未知原生隔离模式")), nil
	}
	instructionGuard.Lock()
	defer instructionGuard.Unlock()
	instructionGuard.enabled = false
	_ = os.Remove(filepath.Join(instructionGuardDir, "policy"))
	effective := "freeze"
	if p.Enabled && p.Mode == "instruction" {
		instructionGuard.keywords = append([]string(nil), p.Keywords...)
		if err := writeInstructionLeaseLocked(); err != nil {
			log.Printf("⚠️ [native-guard] 指令隔离不可用，回退冻结兼容路径：%v", err)
		} else {
			instructionGuard.enabled = true
			effective = "instruction"
		}
	}
	nativeGuardEnabled.Store(p.Enabled)
	data, _ := json.Marshal(map[string]any{"mode": effective, "enabled": p.Enabled})
	log.Printf("🛡️ [rpc] native_guard: enabled=%v mode=%s", p.Enabled, effective)
	return connect.DataResponse(data), nil
}

// nativeGuardRelease 服务端判断本轮不接管时，立刻放行被本地预冻结的原生助手。
func nativeGuardRelease(req connect.Request) (connect.Response, error) {
	reason := "server"
	if req.Payload != nil {
		var p struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(*req.Payload, &p); err == nil && p.Reason != "" {
			reason = p.Reason
		}
	}
	// 标记"服务端已处理"，避免 30 秒兜底再解冻一次。
	nativeGuardClaimed.Store(true)
	if err := utils.UnfreezeProcess(nativeAssistantName); err != nil {
		log.Printf("⚠️ [rpc] native_guard_release 失败: %v", err)
		return connect.ErrorResponse(req.ID, err), nil
	}
	log.Printf("☀️ [rpc] native_guard_release: 已释放 %s（%s）", nativeAssistantName, reason)
	return connect.SuccessResponse(), nil
}

// armNativeGuardSafety 兜底：预冻结后 30 秒内服务端既没确认接管、也没 release，
// 说明服务端可能崩了/断线了，主动解冻，别把原生语音永久冻住。
func armNativeGuardSafety() {
	gen := nativeGuardGen.Add(1)
	nativeGuardClaimed.Store(false)
	time.AfterFunc(30*time.Second, func() {
		if nativeGuardGen.Load() != gen || nativeGuardClaimed.Load() {
			return
		}
		if err := utils.UnfreezeProcess(nativeAssistantName); err == nil {
			log.Printf("🧹 [native-guard] 预冻结 30s 未被服务端确认，已自动解冻 %s", nativeAssistantName)
		}
	})
}

// startNativeProbe 预冻结后采样 4 秒设备状态，发给服务端落日志。
// 用来回答"到底是谁在放 TTS、冻结有没有生效、音乐什么时候被停"。
func startNativeProbe(frozenPID int) {
	go func() {
		start := time.Now()
		mgr := connect.GetMessageManager()
		for {
			elapsed := time.Since(start)
			if elapsed > 4*time.Second {
				return
			}
			raw := ""
			if res, err := utils.RunShellWithTimeout(
				`ubus -t 1 call mediaplayer player_get_play_status 2>/dev/null`, 1500*time.Millisecond); err == nil && res != nil {
				raw = strings.TrimSpace(res.Stdout)
			}
			playerPID, playerStopped := utils.NativePlaybackState()
			aivsPID, aivsStopped := utils.NativeProcessState(nativeAssistantName)
			mibrainPID, mibrainStopped := utils.NativeProcessState("mibrain_service")
			_ = mgr.SendEvent("native_probe", map[string]any{
				"t_ms":            elapsed.Milliseconds(),
				"player_raw":      raw,
				"player_pid":      playerPID,
				"player_stopped":  playerStopped,
				"aivs_pid":        aivsPID,
				"aivs_stopped":    aivsStopped,
				"mibrain_pid":     mibrainPID,
				"mibrain_stopped": mibrainStopped,
				"frozen_pid":      frozenPID,
			})
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

// reportNativeProcesses 预冻结时抓一份进程表，确认 TTS 到底挂在哪个进程上。
func reportNativeProcesses() {
	res, err := utils.RunShellWithTimeout(
		`ps w 2>/dev/null | grep -Ei 'aivs|mibrain|tts|player|mips|mico' | head -30`, 2000*time.Millisecond)
	if err != nil || res == nil {
		return
	}
	_ = connect.GetMessageManager().SendEvent("native_procs", map[string]any{
		"ps": strings.TrimSpace(res.Stdout),
	})
}

// parseAuthFromURL 从 URL 查询参数解析认证信息，支持 username/password 或 u/p
func parseAuthFromURL(serverURL string) (username, password string) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", ""
	}
	q := u.Query()
	username = q.Get("username")
	if username == "" {
		username = q.Get("u")
	}
	password = q.Get("password")
	if password == "" {
		password = q.Get("p")
	}
	return username, password
}

func (c *AppClient) run() {
	flag.Parse()

	if flag.NArg() < 1 {
		log.Fatal("❌ 请输入服务器地址，例如: ./client ws://192.168.31.227:4399")
	}
	serverURLs := flag.Args()
	switchMode := *flagSwitch
	switchKeywords := parseSwitchKeywords(*flagSwitchKeywords)

	if err := audio.GetRecorder().SetDefaultBackend(*flagCaptureBackend); err != nil {
		log.Fatalf("❌ %v", err)
	}
	if switchMode {
		log.Printf("📡 切换模式：服务器列表 %v，切换词 %v", serverURLs, switchKeywords)
	} else {
		log.Printf("📡 远程连接模式：按顺序尝试 %v", serverURLs)
	}
	log.Printf("🎙️ 音频采集后端: %s", *flagCaptureBackend)
	log.Println("✅ 已启动")

	// 服务端唤醒模式：固件用 dsnoop 共享采集，我们可以再开一路 arecord 与它同时收音。
	// 音频推给服务端做唤醒词识别（电脑算力充足，模型可以比音箱本地大得多）。
	if *flagSpeechHost {
		h, err := speechhost.Start(*flagSpeechSocket, func(activate uint64) {
			_ = connect.GetMessageManager().SendEvent("native_wake", map[string]uint64{"activate": activate})
		}, func(pcm []byte) {
			// mips 一次给 60ms（1920B），按服务端习惯切成 20ms 帧
			for len(pcm) >= 640 {
				_ = connect.GetMessageManager().SendStream("mic", pcm[:640], nil)
				pcm = pcm[640:]
			}
		})
		if err != nil {
			log.Fatalf("❌ 语音宿主启动失败: %v", err)
		}
		speechHost = h
		defer h.Close()
		log.Printf("🎙️ 语音宿主模式：原生助手已让出语音通道")
	}

	if *flagMicStream && !*flagSpeechHost {
		streamer, err := audio.StartMicStream(*flagMicDevice, func(chunk []byte) error {
			// 还没连上服务端时丢弃即可，不刷日志
			_ = connect.GetMessageManager().SendStream("mic", chunk, nil)
			return nil
		})
		if err != nil {
			log.Printf("⚠️ 麦克风推流启动失败: %v", err)
		} else {
			defer streamer.Stop()
		}
	}

	var currentIndex int
	reconnectDelay := 1 * time.Second
	for {
		var conn *websocket.Conn
		var serverURL string

		if switchMode {
			// 切换模式：连接 serverURLs[currentIndex]，轮询
			u := serverURLs[currentIndex%len(serverURLs)]
			username, password := parseAuthFromURL(u)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			wsConn, err := c.connectWS(ctx, u, username, password)
			cancel()
			if err != nil {
				log.Printf("⚠️ 连接失败 %s: %v，%v 后重试", u, err, reconnectDelay)
				reconnectDelay = backoffSleep(reconnectDelay)
				continue
			}
			conn = wsConn
			serverURL = u
			reconnectDelay = 1 * time.Second // 成功后重置退避
		} else {
			// 远程连接模式：按顺序尝试，直到成功
			for _, u := range serverURLs {
				username, password := parseAuthFromURL(u)
				if username != "" && password != "" {
					log.Println("🔐 已启用认证（从 URL 解析）")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				wsConn, err := c.connectWS(ctx, u, username, password)
				cancel()
				if err == nil {
					conn = wsConn
					serverURL = u
					reconnectDelay = 1 * time.Second // 成功后重置退避
					break
				}
				log.Printf("⚠️ 连接失败 %s: %v，尝试下一个", u, err)
			}
			if conn == nil {
				log.Printf("⚠️ 所有地址均失败，%v 后重试", reconnectDelay)
				reconnectDelay = backoffSleep(reconnectDelay)
				continue
			}
		}

		if conn == nil {
			reconnectDelay = backoffSleep(reconnectDelay)
			continue
		}
		log.Printf("✅ 已连接: %s", serverURL)

		var cfg *runConfig
		if switchMode {
			switchRequested := make(chan struct{}, 1)
			cfg = &runConfig{
				switchMode:      true,
				switchKeywords:  switchKeywords,
				switchRequested: switchRequested,
			}
			c.init(conn, cfg)

			done := make(chan struct{})
			go func() {
				_ = connect.GetMessageManager().ProcessMessages()
				close(done)
			}()

			select {
			case <-done:
				// 连接正常断开
			case <-switchRequested:
				// 用户说切换词，主动断开并切换
				log.Println("🔄 正在切换 Server...")
				c.dispose()
				<-done
				currentIndex++
			}
			c.dispose() // 切换模式下已在 case 中 dispose，此处对已停止的 monitor 再调一次无副作用
		} else {
			c.init(conn, nil)
			if err := connect.GetMessageManager().ProcessMessages(); err != nil {
				log.Printf("❌ 消息处理异常: %v", err)
			}
			c.dispose()
		}
		log.Println("❌ 已断开连接")
	}
}

// backoffSleep 执行指数退避睡眠，返回下一次应使用的 delay（用于下次调用）。
// 初始 delay 1s，最大 30s，每次翻倍。
func backoffSleep(delay time.Duration) time.Duration {
	time.Sleep(delay)
	next := delay * 2
	if next > 30*time.Second {
		next = 30 * time.Second
	}
	return next
}

func parseSwitchKeywords(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"小智模式", "对话模式"}
	}
	return out
}

// --- RPC Handlers ---

func getVersion(_ connect.Request) (connect.Response, error) {
	data, _ := json.Marshal(base.VERSION)
	raw := json.RawMessage(data)
	return connect.Response{ID: "0", Data: &raw}, nil
}

func isTTSOrPlayScript(script string) bool {
	return strings.Contains(script, "tts_play.sh") || strings.Contains(script, "miplayer")
}

func runShell(req connect.Request) (connect.Response, error) {
	if req.Payload == nil {
		return connect.Response{}, fmt.Errorf("empty command")
	}
	var script string
	if err := json.Unmarshal(*req.Payload, &script); err != nil {
		return connect.Response{}, fmt.Errorf("parse script: %w", err)
	}
	if n := connect.QuietCount(); connect.IsQuietScript(script) {
		if n%50 == 1 {
			log.Printf("🐚 run_shell(例行状态查询，已执行 %d 次，只每 50 次记一行): %s", n, script)
		}
	} else {
		log.Printf("🐚 run_shell: %s", script)
	}

	var res *utils.CommandResult
	var err error
	if isTTSOrPlayScript(script) {
		res, err = utils.RunShellInterruptible(script, 10*60*time.Second)
	} else {
		res, err = utils.RunShell(script)
	}

	if err != nil {
		log.Printf("❌ run_shell error: %v", err)
		return connect.Response{}, err
	}
	// log.Printf("🐚 run_shell result: exit_code=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	data, _ := json.Marshal(res)
	raw := json.RawMessage(data)
	return connect.Response{ID: "0", Data: &raw}, nil
}

// stopNative 无条件停掉固件原生播放（小爱回复/音乐），用于 AI 接管时避免两套声音重叠。
func stopNative(req connect.Request) (connect.Response, error) {
	// 可选 payload: {"suppress_ms": 2000}。不传默认压 2 秒；传 0 表示立刻解除
	// （服务端在我们自己的音频开播前调用，免得把自己也压掉）。
	ms := 2000
	if req.Payload != nil {
		var p struct {
			SuppressMS int `json:"suppress_ms"`
		}
		if err := json.Unmarshal(*req.Payload, &p); err == nil {
			ms = p.SuppressMS
		}
	}
	log.Printf("⏹️ [rpc] stop_native 收到 (id=%s): suppress=%dms", req.ID, ms)
	utils.SuppressNativePlayback(time.Duration(ms) * time.Millisecond)
	return connect.SuccessResponse(), nil
}

// speechHost 语音宿主（-speech-host 时非空），供 RPC 结束本轮收音。
var speechHost *speechhost.Host

// finishNativeDialog 服务端 VAD 判停后调用：停采集并结束本轮 dialog。
func finishNativeDialog(_ connect.Request) (connect.Response, error) {
	if speechHost != nil {
		speechHost.StopTurn()
	}
	return connect.SuccessResponse(), nil
}

// freezeNative 冻结固件原生播放器（mediaplayer）。
// 原生 TTS 和音乐都走它；我们自己的 TTS 走 miplayer，不受影响。
func freezeNative(req connect.Request) (connect.Response, error) {
	name := utils.NativePlayerName
	if req.Payload != nil {
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(*req.Payload, &p); err == nil && p.Name != "" {
			name = p.Name
		}
	}
	pid, err := utils.FreezeProcess(name)
	if err != nil {
		log.Printf("❄️ [rpc] freeze_native 失败 (id=%s): %v", req.ID, err)
		return connect.ErrorResponse(req.ID, err), nil
	}
	if name == nativeAssistantName {
		// 服务端确认接管：后续由它负责解冻，30 秒兜底计时器不要再插手。
		nativeGuardClaimed.Store(true)
	}
	log.Printf("❄️ [rpc] freeze_native 完成 (id=%s): name=%s pid=%d", req.ID, name, pid)
	data, _ := json.Marshal(map[string]any{"pid": pid})
	return connect.DataResponse(json.RawMessage(data)), nil
}

// unfreezeNative 解冻 mediaplayer。payload 可选 {"pid": N}，不传就按进程名查找。
func unfreezeNative(req connect.Request) (connect.Response, error) {
	pid := 0
	name := ""
	if req.Payload != nil {
		var p struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(*req.Payload, &p)
		pid = p.PID
		name = p.Name
	}
	var err error
	if name != "" {
		err = utils.UnfreezeProcess(name)
	} else {
		err = utils.UnfreezeNativePlayback(pid)
	}
	if err != nil {
		log.Printf("☀️ [rpc] unfreeze_native 失败 (id=%s): %v", req.ID, err)
		return connect.ErrorResponse(req.ID, err), nil
	}
	log.Printf("☀️ [rpc] unfreeze_native 完成 (id=%s): pid=%d", req.ID, pid)
	return connect.SuccessResponse(), nil
}

// nativePlaybackState 查询 mediaplayer 的 pid 与冻结状态，供服务端复核。
func nativePlaybackState(req connect.Request) (connect.Response, error) {
	name := utils.NativePlayerName
	if req.Payload != nil {
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(*req.Payload, &p); err == nil && p.Name != "" {
			name = p.Name
		}
	}
	pid, stopped := utils.NativeProcessState(name)
	data, _ := json.Marshal(map[string]any{"pid": pid, "stopped": stopped, "running": pid > 0})
	return connect.DataResponse(json.RawMessage(data)), nil
}

func stopTTS(req connect.Request) (connect.Response, error) {
	start := time.Now()
	log.Printf("⏹️ [rpc] stop_tts 收到 (id=%s): 终止当前 TTS/播放", req.ID)
	utils.StopTTS()
	log.Printf("✅ [rpc] stop_tts 完成 (id=%s, 耗时 %v)", req.ID, time.Since(start).Round(time.Millisecond))
	return connect.SuccessResponse(), nil
}

func startPlay(req connect.Request) (connect.Response, error) {
	var config *audio.AudioConfig
	if req.Payload != nil {
		var cfg audio.AudioConfig
		if err := json.Unmarshal(*req.Payload, &cfg); err == nil {
			config = &cfg
		}
	}
	if err := audio.GetPlayer().Start(config); err != nil {
		return connect.Response{}, err
	}
	return connect.SuccessResponse(), nil
}

func stopPlay(_ connect.Request) (connect.Response, error) {
	if err := audio.GetPlayer().Stop(); err != nil {
		return connect.Response{}, err
	}
	return connect.SuccessResponse(), nil
}

// drainNativePlayer 温和地结束原生播放：本地一次 shell 里先 SIGCONT 再 stop，
// 中间不经过服务端往返，尽量缩小"解冻后小爱先冒一声"的窗口。
func drainNativePlayer(_ connect.Request) (connect.Response, error) {
	script := `MP=$(pidof mediaplayer); ` +
		`if [ -n "$MP" ]; then kill -CONT $MP 2>/dev/null; fi; ` +
		`ubus -t 2 call mediaplayer player_play_operation '{"action":"stop"}' >/dev/null 2>&1; true`
	if _, err := utils.RunShellWithTimeout(script, 4*time.Second); err != nil {
		log.Printf("🎚️ [rpc] drain_native_player 失败: %v", err)
		return connect.Response{}, err
	}
	log.Printf("🎚️ [rpc] drain_native_player：已解冻并停止原生播放")
	return connect.SuccessResponse(), nil
}

// nativeContinueDialog 让固件原生助手重新打开一次收音（ASR-only 续听）。
// 用 pnshelper 的 src=1,event=0：实测会新建一个 Recognize 会话，由原生 VAD 判停。
func nativeContinueDialog(_ connect.Request) (connect.Response, error) {
	if _, err := utils.RunShellWithTimeout(
		`ubus -t 2 call pnshelper event_notify '{"src":1,"event":0}'`, 3*time.Second); err != nil {
		log.Printf("🎤 [rpc] native_continue_dialog 失败: %v", err)
		return connect.Response{}, err
	}
	log.Printf("🎤 [rpc] native_continue_dialog：已请求原生续听")
	return connect.SuccessResponse(), nil
}

func (c *AppClient) startRecording(req connect.Request) (connect.Response, error) {
	var config *audio.AudioConfig
	if req.Payload != nil {
		var cfg audio.AudioConfig
		if err := json.Unmarshal(*req.Payload, &cfg); err == nil {
			config = &cfg
		}
	}
	if config == nil && audio.GetRecorder().DefaultBackend() == "usock" {
		config = &audio.AudioConfig{Backend: "usock"}
	}
	if config != nil && config.Backend == "" && audio.GetRecorder().DefaultBackend() == "usock" {
		config.Backend = "usock"
	}
	if config != nil && config.Backend == "usock" {
		if err := c.enableUsock(); err != nil {
			log.Printf("⚠️ speech.usock 接管失败，录音回退 noop: %v", err)
			config.Backend = "noop"
		}
	}
	err := audio.GetRecorder().StartRecording(func(data []byte) error {
		return connect.GetMessageManager().SendStream("record", data, nil)
	}, config)
	if err != nil {
		return connect.Response{}, err
	}
	return connect.SuccessResponse(), nil
}

func stopRecording(_ connect.Request) (connect.Response, error) {
	if err := audio.GetRecorder().StopRecording(); err != nil {
		return connect.Response{}, err
	}
	return connect.SuccessResponse(), nil
}

func continueDialog(_ connect.Request) (connect.Response, error) {
	if err := audio.GetRecorder().ContinueDialog(); err != nil {
		return connect.Response{}, err
	}
	return connect.SuccessResponse(), nil
}

func finishDialog(_ connect.Request) (connect.Response, error) {
	if err := audio.GetRecorder().FinishDialog(); err != nil {
		return connect.Response{}, err
	}
	return connect.SuccessResponse(), nil
}

// --- Event/Stream Handlers ---

func onEvent(event connect.Event) error {
	// log.Printf("🔥 收到事件: %+v", event)
	// 不记录每条事件，避免老设备上频繁 log I/O 占用 CPU/存储
	return nil
}

func onStream(stream connect.Stream) error {
	if stream.Tag == "play" {
		return audio.GetPlayer().Play(stream.Bytes)
	}
	return nil
}

func main() {
	// 启动自愈：上一次可能是接管途中被杀，mediaplayer 还停在 SIGSTOP。
	// 这里补一次 SIGCONT（幂等，正常播放时是空操作），避免音乐一直不响。
	if pid, stopped := utils.NativePlaybackState(); pid > 0 && stopped {
		if err := utils.UnfreezeNativePlayback(pid); err != nil {
			log.Printf("⚠️ 启动自愈：解冻 mediaplayer(pid=%d) 失败: %v", pid, err)
		} else {
			log.Printf("🧹 启动自愈：mediaplayer(pid=%d) 停在冻结状态，已解冻", pid)
		}
	}
	NewAppClient().run()
}
