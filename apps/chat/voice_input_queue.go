package main

import (
	"context"
	"strings"
	"sync"
)

// 只把明确的独立停止语句当成硬打断；“停止音乐”等动作仍交给现有工具路由。
func isVoiceStopRequest(text string, cfg *AppConfig) bool {
	text = stripWakeWords(text, []string{"小爱同学"})
	text = strings.TrimSpace(stripPunct(stripWakePrefix(text, cfg, nil)))
	switch text {
	case "闭嘴", "别说了", "不要说了", "停止回答", "停止说话", "暂停回答", "取消回答":
		return true
	}
	return false
}

func (e *Engine) voiceContinuationListening() bool {
	e.mu.Lock()
	q := e.voiceInputs
	e.mu.Unlock()
	return q != nil && q.listening() && !e.speaker.Speaking() && !e.XiaoaiSpeaking()
}

type voiceInput struct {
	Text     string
	Revision uint64
}

type voiceInputContextKey struct{}

// voiceInputQueue 保存同一次语音回答出声前的补充，按到达顺序交给 Codex。
// 修订号同时使旧文本失效；首句提交 TTS 与追加输入在同一把锁上互斥。
type voiceInputQueue struct {
	mu       sync.Mutex
	ctx      context.Context
	wake     chan struct{}
	pending  []voiceInput
	revision uint64
	closed   bool
	spoken   bool
}

func newVoiceInputQueue(ctx context.Context) *voiceInputQueue {
	return &voiceInputQueue{ctx: ctx, wake: make(chan struct{}, 1), revision: 1}
}

func voiceInputsFromContext(ctx context.Context) *voiceInputQueue {
	q, _ := ctx.Value(voiceInputContextKey{}).(*voiceInputQueue)
	return q
}

func (q *voiceInputQueue) append(text string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.spoken || q.ctx.Err() != nil {
		return false
	}
	q.revision++
	q.pending = append(q.pending, voiceInput{Text: text, Revision: q.revision})
	q.signalLocked()
	return true
}

func (q *voiceInputQueue) signalLocked() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *voiceInputQueue) pop() (voiceInput, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return voiceInput{}, false
	}
	input := q.pending[0]
	q.pending = q.pending[1:]
	return input, true
}

func (q *voiceInputQueue) notifyPending() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) > 0 {
		q.signalLocked()
	}
}

// nextOrSeal 在回合结束和第二句到达的竞争中，保证消息有且只有一个接收方。
// 有积压就开下一轮；没有则封口，之后的新消息交给引擎正常开启新轮。
func (q *voiceInputQueue) nextOrSeal() (voiceInput, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) > 0 {
		input := q.pending[0]
		q.pending = q.pending[1:]
		return input, true
	}
	q.closed = true
	return voiceInput{}, false
}

func (q *voiceInputQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}

func (q *voiceInputQueue) listening() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.closed && !q.spoken && q.ctx.Err() == nil
}

func (q *voiceInputQueue) current(revision uint64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return revision == q.revision && q.ctx.Err() == nil
}

func (q *voiceInputQueue) latest() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.revision
}

// beginSpeech 在提交合成前最后检查修订号；失败时不合成、也不播放旧回答。
func (q *voiceInputQueue) beginSpeech(revision uint64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if revision != q.revision || q.ctx.Err() != nil {
		return false
	}
	q.spoken = true
	return true
}
