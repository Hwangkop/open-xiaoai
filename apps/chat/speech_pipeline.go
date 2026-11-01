package main

// 语音流水线：文本一边流出来，TTS 一边合成，播放按顺序进行。
//
// 之前是串行：合成第 1 句 → 播完 → 才合成第 2 句，句与句之间空出 1~2 秒的合成时间。
// 现在每拿到一句就立刻发起合成（并行），播放器按到达顺序等它的音频，边播边等下一句。
//
// 只对豆包 TTS 有意义（本地 tts_play.sh 是即时合成播放，无法预生成）。

import (
	"context"
	"sync"

	"log"
)

type speechSeg struct {
	text string
	url  chan string // 合成完成后写入可播放的 URL；失败写空串
}

type speechPipeline struct {
	sp   *Speaker
	ctx  context.Context
	in   chan *speechSeg
	done chan struct{}
	once sync.Once
}

func newSpeechPipeline(ctx context.Context, sp *Speaker) *speechPipeline {
	p := &speechPipeline{
		sp:   sp,
		ctx:  ctx,
		in:   make(chan *speechSeg, 16),
		done: make(chan struct{}),
	}
	go p.run()
	return p
}

// Add 加入一句话：立刻并行发起合成，播放器按顺序播放。
func (p *speechPipeline) Add(text string) {
	seg := &speechSeg{text: text, url: make(chan string, 1)}

	// 先入队（保证播放顺序 = 文本顺序），再异步合成
	select {
	case p.in <- seg:
	case <-p.ctx.Done():
		return
	}

	go func() {
		url, err := p.sp.SynthesizeURL(seg.text)
		if err != nil {
			log.Printf("⚠️ TTS 合成失败: %v", err)
			seg.url <- ""
			return
		}
		seg.url <- url
	}()
}

// run 按顺序播放。播第 N 句时，第 N+1 句通常已经合成好了。
func (p *speechPipeline) run() {
	defer close(p.done)
	for {
		var seg *speechSeg
		select {
		case s, ok := <-p.in:
			if !ok {
				return
			}
			seg = s
		case <-p.ctx.Done():
			return
		}

		var url string
		select {
		case url = <-seg.url:
		case <-p.ctx.Done():
			return
		}
		if url == "" {
			continue
		}
		// 我们的音频要开播了，先解除对小爱原生播放的压制
		p.sp.ReleaseNativeSuppression()
		if err := p.sp.PlayURL(url, true); err != nil {
			log.Printf("⚠️ 播放失败: %v", err)
			return
		}
	}
}

// Close 表示文本已全部加入，等待剩余句子播完（或 ctx 取消）。
func (p *speechPipeline) Close() {
	p.once.Do(func() { close(p.in) })
	select {
	case <-p.done:
	case <-p.ctx.Done():
	}
}
