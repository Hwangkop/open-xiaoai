package main

import (
	"encoding/json"
	"strings"
)

// agentTextEvent 只承载面向用户的正文。Done 表示该条消息结束，需冲刷末尾未断句文本。
// Progress 与最终回答共用播放队列，但不进入最终回复和普通聊天历史。
type agentTextEvent struct {
	Text     string
	Progress bool
	Done     bool
	Reset    bool // 后续补充已进入模型上下文，清掉此前尚未播出的回答。
	Revision uint64
}

// codexMessageEvents 按 itemId 关联 phase。仅接收 agentMessage，推理及工具事件均不转语音。
type codexMessageEvents map[string]string

func (m codexMessageEvents) consume(msg rpcMessage) (agentTextEvent, bool) {
	switch msg.Method {
	case "item/started", "item/completed":
		var p struct {
			Item struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Phase string `json:"phase"`
			} `json:"item"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || p.Item.Type != "agentMessage" || p.Item.ID == "" {
			return agentTextEvent{}, false
		}
		phase := p.Item.Phase
		if phase == "" {
			phase = m[p.Item.ID]
		}
		m[p.Item.ID] = phase
		if msg.Method == "item/completed" && speakableAgentPhase(phase) {
			return agentTextEvent{Progress: phase == "commentary", Done: true}, true
		}
	case "item/agentMessage/delta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || p.Delta == "" {
			return agentTextEvent{}, false
		}
		phase := m[p.ItemID]
		if speakableAgentPhase(phase) {
			return agentTextEvent{Text: p.Delta, Progress: phase == "commentary"}, true
		}
	}
	return agentTextEvent{}, false
}

func speakableAgentPhase(phase string) bool {
	// 旧版协议没有 phase，保留其最终正文行为；未知新 phase 不自动播报。
	return phase == "" || phase == "final_answer" || phase == "commentary"
}

func recvAgentText(stream aiStream) (agentTextEvent, error) {
	if typed, ok := stream.(interface {
		RecvText() (agentTextEvent, error)
	}); ok {
		return typed.RecvText()
	}
	resp, err := stream.Recv()
	if err != nil || len(resp.Choices) == 0 {
		return agentTextEvent{}, err
	}
	return agentTextEvent{Text: resp.Choices[0].Delta.Content}, nil
}

// agentSpeechText 保持句级流式播报；消息边界处独立断句和情绪，不把进度接到答案上。
// speak 在消费流的同一协程调用，实际播放沿用现有可打断的语音流水线。
type agentSpeechText struct {
	reply    strings.Builder
	pending  strings.Builder
	progress bool
	emotion  string
	speak    func(text, emotion string, progress bool) error
}

func (s *agentSpeechText) add(event agentTextEvent) error {
	if event.Reset {
		s.reply.Reset()
		s.pending.Reset()
		s.progress, s.emotion = false, ""
		return nil
	}
	if event.Progress != s.progress {
		if err := s.flush(); err != nil {
			return err
		}
		s.progress, s.emotion = event.Progress, ""
	}
	if !event.Progress {
		s.reply.WriteString(event.Text)
	}
	s.pending.WriteString(event.Text)
	for {
		head, rest, ok := splitAtFirstEnd(s.pending.String())
		if !ok {
			break
		}
		text, _ := splitEmotionTag(trimForSpeech(head))
		// 沿用原先的短句缓冲策略，等后续文本补全语调。
		if hasSpeakableContent(text) && len([]rune(text)) < 4 {
			break
		}
		s.pending.Reset()
		s.pending.WriteString(rest)
		if err := s.say(head); err != nil {
			return err
		}
	}
	if event.Done {
		if err := s.flush(); err != nil {
			return err
		}
		s.emotion = ""
	}
	return nil
}

func (s *agentSpeechText) flush() error {
	text := s.pending.String()
	s.pending.Reset()
	return s.say(text)
}

func (s *agentSpeechText) say(raw string) error {
	text, tag := splitEmotionTag(trimForSpeech(raw))
	if tag != "" {
		s.emotion = tag
	}
	if !hasSpeakableContent(text) {
		return nil
	}
	return s.speak(text, s.emotion, s.progress)
}
