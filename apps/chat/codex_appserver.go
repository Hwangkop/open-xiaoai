package main

// Codex app-server 常驻客户端（JSON-RPC over stdio）。
//
// 相比每轮起一个 `codex exec`（冷启动 ~2s），常驻进程只在启动时握手一次，
// 之后每轮只是 turn/start，首字延迟 ≈ 直连 API。
//
// 协议（codex 0.156，v2 schema）：
//   → initialize {clientInfo}            ← 一次
//   → initialized  (notification)        ← 一次
//   → thread/start {cwd, baseInstructions, ephemeral}  ← 每个会话一次
//   → turn/start {threadId, input:[{type:"text",text}]}  ← 每轮
//   ← item/agentMessage/delta {delta}    ← 流式增量
//   ← turn/completed                     ← 本轮结束
//
// 同一个 threadId 会保留上下文，就是"codex 的会话记录"。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

type rpcMessage struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type CodexAppServer struct {
	startMu sync.Mutex // 只保护启动流程，避免和 request 用的 mu 互相等待
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *bufio.Writer
	nextID  atomic.Int64
	pending map[int64]chan rpcMessage

	// 一个 app-server 可以同时开多个 thread（会话）。通知按 params.threadId 分发，
	// 所以对话和定时任务可以各用一条 thread 并发跑。
	notifMu sync.RWMutex
	subs    map[string]chan rpcMessage

	threadID string
	cfg      CodexConfig
	started  bool

	// threadMu 串行化"确保主会话存在"：管理台点过"新开会话"后 threadID 为空，
	// 但 app-server 进程还活着，这里要补建一条，不能把空 id 发给 turn/start。
	threadMu sync.Mutex

	// threadFile 把会话 id 落盘，重启后可以 thread/resume 接着聊。
	threadFile string

	// 每个会话一把轮次锁：同一 thread 上不能有两轮同时在跑。
	// 否则"打断旧轮"的 turn/interrupt 可能落到刚发起的**新**轮上，把新轮杀掉。
	turnMu    sync.Mutex
	turnLocks map[string]*sync.Mutex
}

func NewCodexAppServer(cfg CodexConfig) *CodexAppServer {
	if strings.TrimSpace(cfg.Bin) == "" {
		cfg.Bin = "codex"
	}
	// Turn 的超时直接来自这里；不归一化会让 time.After(0) 立刻到期
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90
	}
	return &CodexAppServer{
		pending:   make(map[int64]chan rpcMessage),
		subs:      make(map[string]chan rpcMessage),
		turnLocks: make(map[string]*sync.Mutex),
		cfg:       cfg,
	}
}

// Start 拉起常驻进程并完成握手 + 建会话。
func (a *CodexAppServer) Start(ctx context.Context) error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	if a.isStarted() {
		return nil
	}

	bin := a.cfg.Bin
	if bin == "" {
		bin = "codex"
	}
	cmd := exec.Command(bin, "app-server")
	cmd.Env = os.Environ()
	if home := a.cfg.CodexHome; home != "" {
		cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 codex app-server 失败: %w", err)
	}
	a.mu.Lock()
	a.cmd = cmd
	a.stdin = bufio.NewWriter(stdin)
	a.started = true
	a.mu.Unlock()

	go a.readLoop(stdout)

	// 握手
	if _, err := a.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "open-xiaoai", "title": "Open XiaoAI", "version": "0.1.0"},
	}); err != nil {
		a.kill()
		return fmt.Errorf("initialize 失败: %w", err)
	}
	a.notify("initialized", map[string]any{})

	// 先尝试接上上次的会话（部署/重启后不想失忆）
	if saved := a.loadThreadID(); saved != "" {
		if _, err := a.request(ctx, "thread/resume", map[string]any{"threadId": saved}); err == nil {
			a.threadID = saved
			log.Printf("🧩 codex 会话已恢复（thread=%s）", saved)
			return nil
		} else {
			log.Printf("⚠️ 恢复 codex 会话失败，新建一条: %v", err)
		}
	}

	// 建会话：baseInstructions 就是我们的语音提示词
	id, err := a.StartThread(ctx, a.cfg.BaseInstructions, a.cfg.Workspace)
	if err != nil {
		a.kill()
		return err
	}
	a.threadID = id
	a.saveThreadID(id)
	log.Printf("🧩 codex app-server 就绪（thread=%s）", a.threadID)
	return nil
}

// ThreadOptions thread/start 的可选参数。
// Config 走的是 `codex -c key=value` 那套点分覆盖，可以按会话单独限制工具集：
//
//	"mcp_servers.<server>.enabled_tools": ["play_music", ...]   // 裸工具名
//	"mcp_servers.<server>.default_tools_approval_mode": "approve"
type ThreadOptions struct {
	Cwd            string
	ApprovalPolicy string
	Config         map[string]any
}

// StartThread 新开一条会话（同一个进程里可以有多条）。
func (a *CodexAppServer) StartThread(ctx context.Context, baseInstructions, cwd string) (string, error) {
	return a.StartThreadWithOptions(ctx, baseInstructions, ThreadOptions{Cwd: cwd})
}

// StartThreadWithOptions 同 StartThread，但可以带 approvalPolicy 和 config 覆盖。
func (a *CodexAppServer) StartThreadWithOptions(ctx context.Context, baseInstructions string, opt ThreadOptions) (string, error) {
	params := map[string]any{
		// 必须落盘（false）才能在进程重启后 thread/resume 接上原来的上下文
		"ephemeral":        false,
		"baseInstructions": baseInstructions,
	}
	if opt.Cwd != "" {
		params["cwd"] = opt.Cwd
	}
	if opt.ApprovalPolicy != "" {
		params["approvalPolicy"] = opt.ApprovalPolicy
	}
	if len(opt.Config) > 0 {
		params["config"] = opt.Config
	}
	res, err := a.request(ctx, "thread/start", params)
	if err != nil {
		return "", fmt.Errorf("thread/start 失败: %w", err)
	}
	var parsed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(res, &parsed)
	if parsed.Thread.ID == "" {
		return "", fmt.Errorf("thread/start 没有返回 thread id")
	}
	return parsed.Thread.ID, nil
}

func (a *CodexAppServer) readLoop(stdout interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.ID != nil {
			a.mu.Lock()
			ch := a.pending[*msg.ID]
			delete(a.pending, *msg.ID)
			a.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		// 通知：按 threadId 分发给对应的会话
		var head struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(msg.Params, &head)
		a.notifMu.RLock()
		ch := a.subs[head.ThreadID]
		a.notifMu.RUnlock()
		if ch != nil {
			select {
			case ch <- msg:
			default: // 慢消费者丢事件，别阻塞读循环
			}
		}
	}
	log.Printf("⚠️ codex app-server 已退出")
	a.mu.Lock()
	a.started = false
	a.mu.Unlock()
}

func (a *CodexAppServer) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := a.nextID.Add(1)
	ch := make(chan rpcMessage, 1)
	a.mu.Lock()
	a.pending[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	if err := a.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("%s 超时", method)
	}
}

func (a *CodexAppServer) notify(method string, params any) {
	_ = a.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (a *CodexAppServer) write(v any) error {
	a.mu.Lock()
	if a.stdin == nil { // 正在重启/已退出：直接报错，别对着 nil 写（会 panic）
		a.mu.Unlock()
		return fmt.Errorf("codex app-server 未运行")
	}
	enc := json.NewEncoder(a.stdin)
	err := enc.Encode(v)
	if err == nil {
		err = a.stdin.Flush()
	}
	a.mu.Unlock()
	return err
}

// Turn 在默认会话上跑一轮。
func (a *CodexAppServer) Turn(ctx context.Context, text string, onDelta func(string)) (string, error) {
	threadID, err := a.ensureThread(ctx)
	if err != nil {
		return "", err
	}
	return a.TurnOn(ctx, threadID, text, onDelta)
}

// ensureThread 确保主会话存在。
//
// 管理台「智能体」页点“新开会话”只把 threadID 清空（app-server 进程不动），
// 老版本 Turn 看到 started=true 就直接拿空 id 发 turn/start，结果是：
// turn/start: invalid thread id ... found 0，然后每一轮都降级到没有工具的直连模型，
// 语音里点歌/设提醒全失效，直到重启进程。这里负责把会话补回来。
func (a *CodexAppServer) ensureThread(ctx context.Context) (string, error) {
	a.threadMu.Lock()
	defer a.threadMu.Unlock()

	if err := a.Start(ctx); err != nil {
		return "", err
	}
	if id := a.CurrentThread(); id != "" {
		return id, nil
	}

	id, err := a.StartThread(ctx, a.cfg.BaseInstructions, a.cfg.Workspace)
	if err != nil {
		return "", fmt.Errorf("新建主会话失败: %w", err)
	}
	a.mu.Lock()
	// 并发时以先写进去的为准，避免两个请求各建一条
	if a.threadID == "" {
		a.threadID = id
	} else {
		id = a.threadID
	}
	a.mu.Unlock()
	a.saveThreadID(id)
	log.Printf("🧩 codex 新会话（thread=%s）", id)
	return id, nil
}

// TurnOn 在指定会话上跑一轮，onDelta 收到流式增量。返回完整文本。
func (a *CodexAppServer) TurnOn(ctx context.Context, threadID, text string, onDelta func(string)) (string, error) {
	return a.TurnOnOpts(ctx, threadID, text, onDelta, nil)
}

// TurnOnOpts 同 TurnOn，可附加每轮参数（model / effort / summary 等）。
func (a *CodexAppServer) TurnOnOpts(ctx context.Context, threadID, text string, onDelta func(string), opts map[string]any) (string, error) {
	if err := a.Start(ctx); err != nil {
		return "", err
	}
	if threadID == "" {
		return "", fmt.Errorf("会话还没建立（thread id 为空）")
	}
	// 同一会话串行：先拿到这把锁，再发 turn/start。
	// 等待期间如果调用方已经放弃（ctx 取消），立刻退出，别干等上一轮跑完。
	lock := a.threadLock(threadID)
	for !lock.TryLock() {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer lock.Unlock()

	return a.turnOnce(ctx, threadID, text, onDelta, opts)
}

// turnOnce 跑一轮（不含重试和会话锁）。
func (a *CodexAppServer) turnOnce(ctx context.Context, threadID, text string, onDelta func(string), opts map[string]any) (string, error) {
	ch := make(chan rpcMessage, 256)
	a.notifMu.Lock()
	a.subs[threadID] = ch
	a.notifMu.Unlock()
	defer func() {
		a.notifMu.Lock()
		if a.subs[threadID] == ch {
			delete(a.subs, threadID)
		}
		a.notifMu.Unlock()
	}()

	start := time.Now()
	turnParams := map[string]any{
		"threadId": threadID,
		"input":    []any{map[string]any{"type": "text", "text": text}},
	}
	for k, v := range opts {
		turnParams[k] = v
	}
	res, err := a.request(ctx, "turn/start", turnParams)
	if err != nil {
		return "", err
	}
	turnID := extractTurnID(res)

	var sb []byte
	firstMs := int64(0)
	deadline := time.After(time.Duration(a.cfg.Timeout) * time.Second)
	// 首字看门狗：模型卡住时别让用户干等满 60s，早断早重试
	firstTokenTimer := time.NewTimer(a.firstTokenTimeout())
	defer firstTokenTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			// 调用方放弃了这一轮（用户又说话了 / 被打断）。必须通知 app-server 中断，
			// 否则这个回合会继续占着 thread，下一轮排在它后面 → 越来越慢、最后超时。
			a.interruptTurn(threadID, turnID)
			return string(sb), ctx.Err()
		case <-firstTokenTimer.C:
			if firstMs == 0 {
				log.Printf("⚠️ codex 首字超时（%s），中断这一轮", a.firstTokenTimeout())
				a.interruptTurn(threadID, turnID)
				return string(sb), errFirstTokenTimeout
			}
		case <-deadline:
			// app-server 不会因为这一轮超时就把回合丢掉：卡住的那轮会继续占着
			// thread，后面每一轮都排在它后面 → 表现为"越来越慢、接连超时"。
			// 所以这里直接重启进程，下次 Turn 会重新握手 + 新建会话。
			log.Printf("⚠️ codex turn 超时（%ds），中断并重启 app-server 清掉卡住的回合", a.cfg.Timeout)
			a.interruptTurn(threadID, turnID)
			a.clearThreadID() // 这条会话可能已经不对劲，下次新建，避免复制坏状态
			a.Restart()
			return string(sb), fmt.Errorf("turn 超时")
		case msg := <-ch:
			switch {
			case msg.Method == "item/agentMessage/delta":
				var p struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal(msg.Params, &p)
				if p.Delta != "" {
					if firstMs == 0 {
						firstMs = time.Since(start).Milliseconds()
					}
					sb = append(sb, p.Delta...)
					if onDelta != nil {
						onDelta(p.Delta)
					}
				}
			case msg.Method == "turn/completed":
				log.Printf("🧩 codex 回复 %d 字（首字 %dms，总 %dms）", len([]rune(string(sb))), firstMs, time.Since(start).Milliseconds())
				return string(sb), nil
			default:
				// 工具调用、item 事件等——说明模型还在干活，只是没吐字。
				// 不重置的话，带工具的一轮会被"首字看门狗"误杀（实测 12s 被砍）。
				if firstMs == 0 {
					if !firstTokenTimer.Stop() {
						select {
						case <-firstTokenTimer.C:
						default:
						}
					}
					firstTokenTimer.Reset(a.firstTokenTimeout())
				}
			}
		}
	}
}

// extractTurnID 从 turn/start 的返回值里取回合 id（不同小版本字段名可能不同）。
func extractTurnID(res json.RawMessage) string {
	var v struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal(res, &v); err != nil {
		return ""
	}
	if v.Turn.ID != "" {
		return v.Turn.ID
	}
	return v.TurnID
}

// defaultFirstTokenTimeout 多久没见到第一个字就认为卡住了（可被 codex.first_token_seconds 覆盖）。
// 调工具时不吐 delta，所以这个值不能太小——12 秒误杀过正常对话。
const defaultFirstTokenTimeout = 20 * time.Second

func (a *CodexAppServer) firstTokenTimeout() time.Duration {
	if a.cfg.FirstTokenSeconds > 0 {
		return time.Duration(a.cfg.FirstTokenSeconds) * time.Second
	}
	return defaultFirstTokenTimeout
}

var errFirstTokenTimeout = fmt.Errorf("首字超时")

func (a *CodexAppServer) isStarted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

func (a *CodexAppServer) kill() {
	if a.cmd != nil && a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
	}
	a.started = false
}

// threadLock 返回某个会话的轮次锁（懒创建）。
func (a *CodexAppServer) threadLock(threadID string) *sync.Mutex {
	if threadID == "" {
		threadID = "_default"
	}
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.turnLocks == nil {
		a.turnLocks = make(map[string]*sync.Mutex)
	}
	m := a.turnLocks[threadID]
	if m == nil {
		m = &sync.Mutex{}
		a.turnLocks[threadID] = m
	}
	return m
}

// interruptTurn 让 app-server 停下指定会话里正在跑的那一轮。
// 失败不影响主流程（最坏情况就是继续排队，走超时重启兜底）。
func (a *CodexAppServer) interruptTurn(threadID, turnID string) {
	if threadID == "" || turnID == "" {
		return // 没有 turnId 时服务端会拒绝（实测 missing field `turnId`）
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := a.request(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}); err != nil {
		log.Printf("⚠️ turn/interrupt 失败: %v", err)
		return
	}
	log.Printf("⏹️ 已中断 codex 回合（thread=%s）", threadID)
}

// CurrentThread 返回主对话 thread id（可能为空）。
func (a *CodexAppServer) CurrentThread() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.threadID
}

// ResetMainThread 丢掉主对话会话（下次 Turn 会新建），并清掉落盘 id。
func (a *CodexAppServer) ResetMainThread() {
	a.mu.Lock()
	a.threadID = ""
	a.mu.Unlock()
	a.clearThreadID()
	log.Printf("🧩 主对话会话已重置，下次对话新建 thread")
}

// ListTurns 读会话里的轮次列表（用来在管理台显示"多少轮上下文"）。
func (a *CodexAppServer) ListTurns(ctx context.Context, threadID string) (json.RawMessage, error) {
	if threadID == "" {
		return nil, fmt.Errorf("会话还没建立")
	}
	return a.request(ctx, "thread/turns/list", map[string]any{"threadId": threadID})
}

// CompactThread 让 codex 压缩指定会话的历史（thread/compact）。
func (a *CodexAppServer) CompactThread(ctx context.Context, threadID string) error {
	if threadID == "" {
		return fmt.Errorf("会话还没建立")
	}
	// 方法名是 thread/compact/start（从一个报错里把整张方法表打出来后确认的）
	_, err := a.request(ctx, "thread/compact/start", map[string]any{"threadId": threadID})
	return err
}

// SetThreadFile 设置会话 id 的落盘路径（和 config.yaml 同目录）。
func (a *CodexAppServer) SetThreadFile(path string) {
	a.mu.Lock()
	a.threadFile = path
	a.mu.Unlock()
}

// loadThreadID 读上次的会话 id；没有就返回空。
func (a *CodexAppServer) loadThreadID() string {
	a.mu.Lock()
	path := a.threadFile
	a.mu.Unlock()
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var v struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return strings.TrimSpace(v.ThreadID)
}

// clearThreadID 丢掉落盘的会话 id（下次启动新建）。
func (a *CodexAppServer) clearThreadID() {
	a.mu.Lock()
	path := a.threadFile
	a.mu.Unlock()
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("⚠️ 清理 codex 会话 id 失败: %v", err)
	}
}

// saveThreadID 记住当前会话，下次启动用它 resume。
func (a *CodexAppServer) saveThreadID(id string) {
	a.mu.Lock()
	path := a.threadFile
	a.mu.Unlock()
	if path == "" || id == "" {
		return
	}
	raw, _ := json.MarshalIndent(map[string]any{
		"thread_id": id,
		"saved_at":  time.Now().Unix(),
	}, "", "  ")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		log.Printf("⚠️ 保存 codex 会话 id 失败: %v", err)
	}
}

// Restart 杀掉 app-server 并清空会话状态，下次 Turn 会重新拉起 + 新建 thread。
// 只用于异常恢复（超时/协议卡死），正常路径不需要。
func (a *CodexAppServer) Restart() {
	a.startMu.Lock()
	defer a.startMu.Unlock()

	a.mu.Lock()
	a.kill()
	a.cmd = nil
	a.stdin = nil
	a.threadID = ""
	a.pending = make(map[int64]chan rpcMessage)
	a.mu.Unlock()

	a.notifMu.Lock()
	a.subs = make(map[string]chan rpcMessage)
	a.notifMu.Unlock()
}

func (a *CodexAppServer) Close() { a.kill() }

// ---------------- 适配成 openai 流 ----------------

type codexAppStream struct {
	ch     chan openai.ChatCompletionStreamResponse
	errs   chan error
	cancel context.CancelFunc
	closed chan struct{}
}

func newCodexAppStream(parent context.Context, srv *CodexAppServer, prompt string) *codexAppStream {
	ctx, cancel := context.WithCancel(parent)
	s := &codexAppStream{
		ch:     make(chan openai.ChatCompletionStreamResponse, 64),
		errs:   make(chan error, 1),
		cancel: cancel,
		closed: make(chan struct{}),
	}
	go func() {
		defer close(s.ch)
		_, err := srv.Turn(ctx, prompt, func(delta string) {
			select {
			case s.ch <- openai.ChatCompletionStreamResponse{
				Choices: []openai.ChatCompletionStreamChoice{
					{Delta: openai.ChatCompletionStreamChoiceDelta{Content: delta}},
				},
			}:
			case <-ctx.Done():
			}
		})
		if err != nil && ctx.Err() == nil {
			s.errs <- err
		}
	}()
	return s
}

func (s *codexAppStream) Recv() (openai.ChatCompletionStreamResponse, error) {
	select {
	case resp, ok := <-s.ch:
		if !ok {
			select {
			case err := <-s.errs:
				return openai.ChatCompletionStreamResponse{}, err
			default:
			}
			return openai.ChatCompletionStreamResponse{}, io.EOF
		}
		return resp, nil
	case <-s.closed:
		return openai.ChatCompletionStreamResponse{}, io.EOF
	}
}

func (s *codexAppStream) Close() error {
	select {
	case <-s.closed:
		return nil
	default:
	}
	close(s.closed)
	s.cancel()
	return nil
}

// CodexThreadRunner 给定时任务这类旁路用途：独立 thread，不污染对话上下文，
// 但复用同一个常驻进程（省掉每轮 exec 的冷启动）。
type CodexThreadRunner struct {
	srv *CodexAppServer

	mu       sync.Mutex
	threadID string
}

func NewCodexThreadRunner(srv *CodexAppServer) *CodexThreadRunner {
	return &CodexThreadRunner{srv: srv}
}

// CurrentThread 返回任务会话 id（可能为空）。
func (r *CodexThreadRunner) CurrentThread() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threadID
}

// Reset 丢掉任务会话，下次 Ask 会新建。
func (r *CodexThreadRunner) Reset() {
	r.mu.Lock()
	r.threadID = ""
	r.mu.Unlock()
}

func (r *CodexThreadRunner) Ask(ctx context.Context, prompt string) (string, error) {
	if err := r.srv.Start(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	if r.threadID == "" {
		id, err := r.srv.StartThread(ctx, r.srv.cfg.BaseInstructions, r.srv.cfg.Workspace)
		if err != nil {
			r.mu.Unlock()
			return "", err
		}
		r.threadID = id
		log.Printf("🧩 任务会话就绪（thread=%s）", id)
	}
	id := r.threadID
	r.mu.Unlock()
	return r.srv.TurnOn(ctx, id, prompt, nil)
}
