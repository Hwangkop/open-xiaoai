package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVoiceInputQueueSpeechAndAppendAreExclusive(t *testing.T) {
	for i := 0; i < 150; i++ {
		q := newVoiceInputQueue(context.Background())
		var appended, spoken bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); appended = q.append("给五岁的孩子听") }()
		go func() { defer wg.Done(); spoken = q.beginSpeech(1) }()
		wg.Wait()
		if appended == spoken {
			t.Fatalf("追加与旧首句播报必须有且仅有一个成功: appended=%v spoken=%v", appended, spoken)
		}
	}
}

func TestVoiceInputQueueCompletionDoesNotLoseInput(t *testing.T) {
	for i := 0; i < 150; i++ {
		q := newVoiceInputQueue(context.Background())
		var appended, consumed bool
		var input voiceInput
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); appended = q.append("不要太吓人") }()
		go func() { defer wg.Done(); input, consumed = q.nextOrSeal() }()
		wg.Wait()
		if appended != consumed || (consumed && input.Text != "不要太吓人") {
			t.Fatalf("结束竞争丢消息: appended=%v consumed=%v input=%+v", appended, consumed, input)
		}
	}
}

// 用真实 JSON-RPC 请求和订阅通道回放后端时序，避免只测试辅助函数。
func fakeVoiceCodex(t *testing.T, handle func(*CodexAppServer, rpcMessage)) *CodexAppServer {
	t.Helper()
	srv := NewCodexAppServer(CodexConfig{Timeout: 5})
	srv.started, srv.threadID = true, "voice-thread"
	r, w := io.Pipe()
	srv.stdin = bufio.NewWriter(w)
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("模拟 Codex 请求循环未退出")
		}
	})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			var req rpcMessage
			if json.Unmarshal(scanner.Bytes(), &req) == nil {
				handle(srv, req)
			}
		}
	}()
	return srv
}

func respondVoiceRPC(srv *CodexAppServer, req rpcMessage, result any, rpcErr *rpcError) {
	if req.ID == nil {
		return
	}
	srv.mu.Lock()
	ch := srv.pending[*req.ID]
	srv.mu.Unlock()
	if ch != nil {
		raw, _ := json.Marshal(result)
		ch <- rpcMessage{Result: raw, Error: rpcErr}
	}
}

func notifyVoiceRPC(srv *CodexAppServer, method string, params any) {
	raw, _ := json.Marshal(params)
	srv.notifMu.RLock()
	ch := srv.subs["voice-thread"]
	srv.notifMu.RUnlock()
	if ch != nil {
		ch <- rpcMessage{Method: method, Params: raw}
	}
}

func voiceItem(srv *CodexAppServer, method, turn, id, kind, text string) {
	item := map[string]any{"id": id, "type": kind, "phase": "final_answer", "text": text}
	if kind == "userMessage" {
		item["content"] = []any{map[string]string{"type": "text", "text": text}}
	}
	notifyVoiceRPC(srv, method, map[string]any{"threadId": "voice-thread", "turnId": turn, "item": item})
}

func voiceDelta(srv *CodexAppServer, turn, item, text string) {
	notifyVoiceRPC(srv, "item/agentMessage/delta", map[string]string{
		"threadId": "voice-thread", "turnId": turn, "itemId": item, "delta": text,
	})
}

func voiceCompleted(srv *CodexAppServer, turn string) {
	notifyVoiceRPC(srv, "turn/completed", map[string]any{"threadId": "voice-thread", "turn": map[string]string{"id": turn, "status": "completed"}})
}

func voiceRPCInput(req rpcMessage) string {
	var p struct {
		Input []struct{ Text string } `json:"input"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if len(p.Input) == 0 {
		return ""
	}
	return p.Input[0].Text
}

func TestCodexSteerWaitsForAppliedInputAndDropsStaleSpeech(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	q := newVoiceInputQueue(ctx)
	ctx = context.WithValue(ctx, voiceInputContextKey{}, q)
	var requests []string
	srv := fakeVoiceCodex(t, func(srv *CodexAppServer, req rpcMessage) {
		requests = append(requests, req.Method)
		switch req.Method {
		case "turn/start":
			respondVoiceRPC(srv, req, map[string]any{"turn": map[string]string{"id": "one"}}, nil)
			voiceItem(srv, "item/started", "one", "old", "agentMessage", "")
			voiceDelta(srv, "one", "old", "旧答案的开头")
		case "turn/steer":
			respondVoiceRPC(srv, req, map[string]string{"turnId": "one"}, nil)
			// 与真机一致：应答成功后还会继续吐旧答案，直到后续 userMessage 落地。
			voiceDelta(srv, "one", "old", "这是不能播的旧结尾。")
			voiceItem(srv, "item/completed", "one", "old", "agentMessage", "旧答案")
			voiceItem(srv, "item/started", "one", "supplement", "userMessage", voiceRPCInput(req))
			voiceDelta(srv, "one", "old", "旧消息的迟到片段。")
			voiceItem(srv, "item/started", "one", "new", "agentMessage", "")
			voiceDelta(srv, "one", "new", "这是适合五岁孩子的故事。")
			voiceItem(srv, "item/completed", "one", "new", "agentMessage", "这是适合五岁孩子的故事。")
			voiceCompleted(srv, "one")
		}
	})
	var spoken []string
	speech := &agentSpeechText{speak: func(text, emotion string, progress bool) error {
		if q.beginSpeech(q.latest()) {
			spoken = append(spoken, text)
		}
		return nil
	}}
	reply, err := srv.TurnWithProgress(ctx, "讲个故事", func(event agentTextEvent) {
		if !q.current(event.Revision) {
			return
		}
		if err := speech.add(event); err != nil {
			t.Error(err)
		}
		if event.Text == "旧答案的开头" && !q.append("给五岁的孩子听") {
			t.Error("首句尚未播报时应能补充")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "这是适合五岁孩子的故事。" || speech.reply.String() != reply || !reflect.DeepEqual(spoken, []string{"这是适合五岁孩子的故事"}) {
		t.Fatalf("旧答案泄漏: reply=%q history=%q spoken=%q", reply, speech.reply.String(), spoken)
	}
	if !reflect.DeepEqual(requests, []string{"turn/start", "turn/steer"}) {
		t.Fatalf("补充不应取消重开或重发原任务: %q", requests)
	}
}

func TestCodexMultipleSteersPreserveOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	q := newVoiceInputQueue(ctx)
	q.append("给五岁孩子听")
	q.append("不要太吓人")
	ctx = context.WithValue(ctx, voiceInputContextKey{}, q)
	var steers []string
	srv := fakeVoiceCodex(t, func(srv *CodexAppServer, req rpcMessage) {
		switch req.Method {
		case "turn/start":
			respondVoiceRPC(srv, req, map[string]any{"turn": map[string]string{"id": "one"}}, nil)
		case "turn/steer":
			text := voiceRPCInput(req)
			steers = append(steers, text)
			respondVoiceRPC(srv, req, map[string]string{"turnId": "one"}, nil)
			voiceItem(srv, "item/started", "one", fmt.Sprint("user", len(steers)), "userMessage", text)
			id := fmt.Sprint("answer", len(steers))
			voiceItem(srv, "item/started", "one", id, "agentMessage", "")
			voiceDelta(srv, "one", id, fmt.Sprintf("已处理第%d次补充。", len(steers)))
			if len(steers) == 2 {
				voiceCompleted(srv, "one")
			}
		}
	})
	var visible strings.Builder
	reply, err := srv.TurnWithProgress(ctx, "讲故事", func(event agentTextEvent) {
		if q.current(event.Revision) {
			if event.Reset {
				visible.Reset()
			}
			visible.WriteString(event.Text)
		}
	})
	if err != nil || reply != "已处理第2次补充。" || visible.String() != reply || !reflect.DeepEqual(steers, []string{"给五岁孩子听", "不要太吓人"}) {
		t.Fatalf("多次补充顺序错误: steers=%q reply=%q visible=%q err=%v", steers, reply, visible.String(), err)
	}
}

func TestCodexRejectedSteerStartsNextTurnAndIgnoresLateCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	q := newVoiceInputQueue(ctx)
	q.append("改讲西游记")
	ctx = context.WithValue(ctx, voiceInputContextKey{}, q)
	var starts []string
	srv := fakeVoiceCodex(t, func(srv *CodexAppServer, req rpcMessage) {
		switch req.Method {
		case "turn/start":
			starts = append(starts, voiceRPCInput(req))
			turn := fmt.Sprint("turn", len(starts))
			respondVoiceRPC(srv, req, map[string]any{"turn": map[string]string{"id": turn}}, nil)
			if len(starts) == 2 {
				voiceCompleted(srv, "turn1") // 迟到的旧完成事件不能结束 turn2。
				voiceItem(srv, "item/started", turn, "new", "agentMessage", "")
				voiceDelta(srv, turn, "new", "接着讲西游记。")
				voiceCompleted(srv, turn)
			}
		case "turn/steer":
			respondVoiceRPC(srv, req, nil, &rpcError{Code: -32600, Message: "no active turn"})
		case "turn/interrupt":
			respondVoiceRPC(srv, req, map[string]any{}, nil)
			voiceCompleted(srv, "turn1")
		}
	})
	reply, err := srv.TurnWithProgress(ctx, "讲个故事", nil)
	if err != nil || reply != "接着讲西游记。" || !reflect.DeepEqual(starts, []string{"讲个故事", "改讲西游记"}) {
		t.Fatalf("转下一轮失败或重复原任务: starts=%q reply=%q err=%v", starts, reply, err)
	}
}

func TestThinkingContinuationReachesEngineWithoutWakeWord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newVoiceInputQueue(ctx)
	e := &Engine{config: &AppConfig{}, speaker: NewSpeaker(), voiceInputs: q, cancelFunc: cancel}
	e.onUtterance("给五岁的小朋友听", UtteranceASR{})
	input, ok := q.pop()
	if !ok || input.Text != "给五岁的小朋友听" || ctx.Err() != nil {
		t.Fatalf("思考期间补充被丢弃或取消原任务: input=%+v ok=%v ctx=%v", input, ok, ctx.Err())
	}
	if e.turnSeq.Load() != 0 || len(e.history) != 1 || e.history[0].Content != input.Text {
		t.Fatal("追加不应创建新轮，且应记录用户补充")
	}
	// 双路同一句仍然去重，不能借着开放续听重复提交。
	e.onUtterance("给五岁的小朋友听", UtteranceASR{})
	if _, ok := q.pop(); ok {
		t.Fatal("同一句双路识别不应重复入队")
	}
}

func TestMusicLyricsRemainIsolatedDuringThinking(t *testing.T) {
	q := newVoiceInputQueue(context.Background())
	s := NewSpeaker()
	s.st.music = true
	s.nativeTakeover.active = true
	e := &Engine{config: &AppConfig{}, speaker: s, voiceInputs: q}
	e.onUtterance("夜空中最亮的星", UtteranceASR{})
	if _, ok := q.pop(); ok {
		t.Fatal("音乐态无唤醒词的歌词不能作为补充")
	}
}

func TestVoiceStopRequestDoesNotSwallowMusicOrQuotedWords(t *testing.T) {
	cfg := &AppConfig{}
	for _, text := range []string{"闭嘴", "小爱同学，停止回答。", "豆包，别说了"} {
		if !isVoiceStopRequest(text, cfg) {
			t.Errorf("应立即打断: %q", text)
		}
	}
	for _, text := range []string{"停止音乐", "闭嘴这个词是什么意思", "不要说了也是一句台词"} {
		if isVoiceStopRequest(text, cfg) {
			t.Errorf("不应误触停止回答: %q", text)
		}
	}
}

// 手动跑：CODEX_VOICE_STEER_TEST=1 CODEX_VOICE_TEST_BIN=... go test -run TestCodexVoiceSteerLive -v .
// 使用真实模型与临时会话验证输入/语音分流；关闭业务工具，不连接音箱播放。
func TestCodexVoiceSteerLive(t *testing.T) {
	if os.Getenv("CODEX_VOICE_STEER_TEST") == "" {
		t.Skip("设置 CODEX_VOICE_STEER_TEST=1 才跑真实模型验证")
	}
	bin := os.Getenv("CODEX_VOICE_TEST_BIN")
	if bin == "" {
		bin = "codex"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "app-server", "-c", "mcp_servers.openxiaoai.enabled=false")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	srv := NewCodexAppServer(CodexConfig{Timeout: 45})
	srv.cmd, srv.stdin, srv.started = cmd, bufio.NewWriter(stdin), true
	go srv.readLoop(stdout)
	if _, err := srv.request(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "xiaoai-voice-steer-test", "version": "0.1"}}); err != nil {
		t.Fatal(err)
	}
	srv.notify("initialized", map[string]any{})
	res, err := srv.request(ctx, "thread/start", map[string]any{
		"ephemeral": true, "baseInstructions": "这是隔离协议测试。不要调用任何工具，只按最后一条用户消息的要求简短回答。",
	})
	if err != nil {
		t.Fatal(err)
	}
	var thread struct {
		Thread struct{ ID string } `json:"thread"`
	}
	if err := json.Unmarshal(res, &thread); err != nil || thread.Thread.ID == "" {
		t.Fatal("临时会话创建失败")
	}
	srv.threadID = thread.Thread.ID
	q := newVoiceInputQueue(ctx)
	q.append("补充：上一条先不用回答，请只回答四个字：补充收到")
	ctx = context.WithValue(ctx, voiceInputContextKey{}, q)
	stream := newCodexAppStream(ctx, srv, "只回答四个字：原始问题")
	defer stream.Close()
	var spoken []string
	revision := uint64(1)
	speech := &agentSpeechText{speak: func(text, emotion string, progress bool) error {
		if q.beginSpeech(revision) {
			spoken = append(spoken, text)
		}
		return nil
	}}
	resets, dropped := 0, 0
	for {
		event, err := recvAgentText(stream)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !q.current(event.Revision) {
			dropped++
			continue
		}
		revision = event.Revision
		if event.Reset {
			resets++
		}
		if err := speech.add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := speech.flush(); err != nil {
		t.Fatal(err)
	}
	t.Logf("应用补充=%d次，过滤旧事件=%d，待播文本=%q", resets, dropped, spoken)
	if resets != 1 || !strings.Contains(speech.reply.String(), "补充收到") || strings.Contains(strings.Join(spoken, ""), "原始问题") || !strings.Contains(strings.Join(spoken, ""), "补充收到") {
		t.Fatalf("真实追加/旧语音过滤失败: reply=%q spoken=%q", speech.reply.String(), spoken)
	}
}
