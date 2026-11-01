package main

// 把豆包网页直连（DoubaoWeb）适配成 openai.ChatCompletionStream 的形态，
// 这样 Engine 里的流式处理逻辑（buffer 成句 → 逐句 TTS → 打断）完全不用改。

import (
	"context"
	"io"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

type doubaoChatStream struct {
	ch     chan openai.ChatCompletionStreamResponse
	errs   chan error
	cancel context.CancelFunc
	closed chan struct{}
}

func newDoubaoChatStream(parent context.Context, web *DoubaoWeb, prompt string) *doubaoChatStream {
	ctx, cancel := context.WithCancel(parent)
	s := &doubaoChatStream{
		ch:     make(chan openai.ChatCompletionStreamResponse, 64),
		errs:   make(chan error, 1),
		cancel: cancel,
		closed: make(chan struct{}),
	}

	go func() {
		defer close(s.ch)
		_, err := web.ChatStream(ctx, prompt, func(delta string) {
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

func (s *doubaoChatStream) Recv() (openai.ChatCompletionStreamResponse, error) {
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

func (s *doubaoChatStream) Close() error {
	select {
	case <-s.closed:
		return nil
	default:
	}
	close(s.closed)
	s.cancel()
	return nil
}

// flattenMessages 把 OpenAI 风格的消息列表压成一段文本。
//
// 豆包网页的 /chat/completion 一次只接受一条用户消息（新会话），
// 所以把系统设定和历史对话拼成上下文段落，最后接当前问题。
func flattenMessages(messages []openai.ChatCompletionMessage) string {
	var systemParts []string
	var turns []string
	current := ""

	for _, m := range messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		switch m.Role {
		case openai.ChatMessageRoleSystem:
			systemParts = append(systemParts, content)
		case openai.ChatMessageRoleUser:
			if current != "" {
				turns = append(turns, "用户："+current)
			}
			current = content
		case openai.ChatMessageRoleAssistant:
			if current != "" {
				turns = append(turns, "用户："+current)
				current = ""
			}
			turns = append(turns, "助手："+content)
		}
	}

	if len(turns) == 0 && len(systemParts) == 0 {
		return current
	}

	var b strings.Builder
	if len(systemParts) > 0 {
		b.WriteString(strings.Join(systemParts, "\n"))
		b.WriteString("\n\n")
	}
	if len(turns) > 0 {
		b.WriteString("以下是此前的对话：\n")
		b.WriteString(strings.Join(turns, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString("用户：")
	b.WriteString(current)
	return b.String()
}
