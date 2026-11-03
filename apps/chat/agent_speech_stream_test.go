package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func agentNotification(method, params string) rpcMessage {
	return rpcMessage{Method: method, Params: json.RawMessage(params)}
}

func TestCodexMessageEventsOnlySpeakUserFacingText(t *testing.T) {
	m := codexMessageEvents{}
	msgs := []rpcMessage{
		agentNotification("item/started", `{"item":{"type":"reasoning","id":"r"}}`),
		agentNotification("item/reasoning/textDelta", `{"itemId":"r","delta":"内部分析"}`),
		agentNotification("item/started", `{"item":{"type":"mcpToolCall","id":"t"}}`),
		agentNotification("item/mcpToolCall/progress", `{"itemId":"t","delta":"工具结果"}`),
		agentNotification("item/started", `{"item":{"type":"agentMessage","id":"p","phase":"commentary"}}`),
		agentNotification("item/agentMessage/delta", `{"itemId":"p","delta":"我先查一下"}`),
		// completed 中的完整 text 是已收到 delta 的重放，不能重复播。
		agentNotification("item/completed", `{"item":{"type":"agentMessage","id":"p","text":"我先查一下"}}`),
		agentNotification("item/started", `{"item":{"type":"agentMessage","id":"f","phase":"final_answer"}}`),
		agentNotification("item/agentMessage/delta", `{"itemId":"f","delta":"查到了。"}`),
		agentNotification("item/completed", `{"item":{"type":"agentMessage","id":"f","text":"查到了。","phase":"final_answer"}}`),
		agentNotification("item/started", `{"item":{"type":"agentMessage","id":"x","phase":"future_private"}}`),
		agentNotification("item/agentMessage/delta", `{"itemId":"x","delta":"未知内容"}`),
		agentNotification("item/agentMessage/delta", `{`),
	}
	var got []agentTextEvent
	for _, msg := range msgs {
		if event, ok := m.consume(msg); ok {
			got = append(got, event)
		}
	}
	want := []agentTextEvent{
		{Text: "我先查一下", Progress: true}, {Progress: true, Done: true},
		{Text: "查到了。"}, {Done: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("正文事件不符: got=%+v want=%+v", got, want)
	}
}

func TestAgentSpeechSeparatesProgressFromAnswer(t *testing.T) {
	var spoken []string
	s := &agentSpeechText{speak: func(text, emotion string, progress bool) error {
		spoken = append(spoken, text)
		return nil
	}}
	for _, event := range []agentTextEvent{
		{Text: "先帮你查", Progress: true}, {Text: "资料", Progress: true},
		{Progress: true, Done: true}, // 没有句号的进度也要立即播。
		{Text: "再核对一下", Progress: true},
		{Text: "这是结果。后续"}, // 即使旧协议缺 completed，切换 phase 也必须断开。
		{Text: "说明"}, {Done: true},
	} {
		if err := s.add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"先帮你查资料", "再核对一下", "这是结果", "后续说明"}; !reflect.DeepEqual(spoken, want) {
		t.Fatalf("播放顺序/重复播报错误: %q", spoken)
	}
	if s.reply.String() != "这是结果。后续说明" {
		t.Fatalf("进度混入聊天历史: %q", s.reply.String())
	}
}

func TestAgentSpeechLegacyFinalAndCancellation(t *testing.T) {
	t.Run("无进度时沿用原来的句级播报", func(t *testing.T) {
		var spoken []string
		s := &agentSpeechText{speak: func(text, emotion string, progress bool) error {
			spoken = append(spoken, text)
			return nil
		}}
		for _, token := range []string{"你好", "呀。第一", "句！末尾"} {
			if err := s.add(agentTextEvent{Text: token}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.flush(); err != nil {
			t.Fatal(err)
		}
		if strings.Join(spoken, "") != "你好呀。第一句！末尾" || s.reply.String() != "你好呀。第一句！末尾" {
			t.Fatalf("旧路径不兼容: spoken=%q reply=%q", spoken, s.reply.String())
		}
	})
	t.Run("打断后不继续提交句子", func(t *testing.T) {
		calls := 0
		s := &agentSpeechText{speak: func(text, emotion string, progress bool) error {
			calls++
			return context.Canceled
		}}
		err := s.add(agentTextEvent{Text: "正在查询资料。还有下一句。", Progress: true})
		if !errors.Is(err, context.Canceled) || calls != 1 || s.reply.Len() != 0 {
			t.Fatalf("打断未生效: calls=%d err=%v", calls, err)
		}
	})
}

// fakeCodexTurn 通过实际 request/subscription 路径注入通知，不启动模型或音箱。
func fakeCodexTurn(t *testing.T, events []rpcMessage, beforeFinal <-chan struct{}) *CodexAppServer {
	t.Helper()
	srv := NewCodexAppServer(CodexConfig{Timeout: 5})
	srv.started, srv.threadID = true, "test-thread"
	r, w := io.Pipe()
	srv.stdin = bufio.NewWriter(w)
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	go func() {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			var req rpcMessage
			if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == nil {
				continue
			}
			srv.mu.Lock()
			pending := srv.pending[*req.ID]
			srv.mu.Unlock()
			if pending != nil {
				pending <- rpcMessage{Result: json.RawMessage(`{"turn":{"id":"test-turn"}}`)}
			}
			srv.notifMu.RLock()
			notifications := srv.subs["test-thread"]
			srv.notifMu.RUnlock()
			for _, event := range events {
				if event.Method == "turn/completed" && beforeFinal != nil {
					select {
					case <-beforeFinal:
					case <-time.After(2 * time.Second):
						return
					}
				}
				notifications <- event
			}
			return
		}
	}()
	return srv
}

func TestCodexProgressReachesSpeechBeforeTurnCompletes(t *testing.T) {
	progressSpoken := make(chan struct{}, 1)
	events := []rpcMessage{
		agentNotification("item/started", `{"item":{"type":"agentMessage","id":"p","phase":"commentary"}}`),
		agentNotification("item/agentMessage/delta", `{"itemId":"p","delta":"我先核对资料"}`),
		agentNotification("item/completed", `{"item":{"type":"agentMessage","id":"p","phase":"commentary"}}`),
		agentNotification("item/reasoning/textDelta", `{"delta":"不应播报"}`),
		agentNotification("item/started", `{"item":{"type":"agentMessage","id":"f","phase":"final_answer"}}`),
		agentNotification("item/agentMessage/delta", `{"itemId":"f","delta":"资料核对完了。"}`),
		agentNotification("turn/completed", `{}`),
	}
	srv := fakeCodexTurn(t, events, progressSpoken)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := newCodexAppStream(ctx, srv, "查资料")
	defer stream.Close()
	var spoken []string
	s := &agentSpeechText{speak: func(text, emotion string, progress bool) error {
		spoken = append(spoken, text)
		if progress {
			progressSpoken <- struct{}{}
		}
		return nil
	}}
	for {
		event, err := recvAgentText(stream)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := s.add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spoken, []string{"我先核对资料", "资料核对完了"}) || s.reply.String() != "资料核对完了。" {
		t.Fatalf("进度/答案串流错误: spoken=%q reply=%q", spoken, s.reply.String())
	}
}

func TestCodexTurnReturnValueExcludesCommentary(t *testing.T) {
	srv := fakeCodexTurn(t, []rpcMessage{
		agentNotification("item/started", `{"item":{"type":"agentMessage","id":"p","phase":"commentary"}}`),
		agentNotification("item/agentMessage/delta", `{"itemId":"p","delta":"正在处理。"}`),
		// 兼容没有 item/started 和 phase 的旧协议。
		agentNotification("item/agentMessage/delta", `{"delta":"最终回复。"}`),
		agentNotification("turn/completed", `{}`),
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var deltas strings.Builder
	reply, err := srv.Turn(ctx, "你好", func(delta string) { deltas.WriteString(delta) })
	if err != nil || reply != "最终回复。" || deltas.String() != reply {
		t.Fatalf("进度污染返回值或旧回调: reply=%q delta=%q err=%v", reply, deltas.String(), err)
	}
}
