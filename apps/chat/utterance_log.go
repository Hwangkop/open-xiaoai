package main

// 最近识别记录：把每条 ASR 结果记下来，管理台可以直接看到"刚才被识别成了什么"，
// 然后一键加进唤醒词表。环形缓冲，保留最近 60 条。

import (
	"sync"
	"time"
)

type utteranceRecord struct {
	Text    string `json:"text"`
	At      int64  `json:"at"`      // unix 毫秒
	Matched bool   `json:"matched"` // 是否命中唤醒词
	Ack     bool   `json:"ack"`     // 命中后是"只应一声"还是当问题处理
	Dialog  bool   `json:"dialog"`  // 对话窗口内的句子（唤醒监听已关闭）
}

var recentUtterances = struct {
	mu    sync.Mutex
	items []utteranceRecord
	max   int
}{max: 60}

func recordUtterance(rec utteranceRecord) {
	// 纯标点/空白（ASR 对噪声的产物）没有参考价值，不记录
	if stripPunct(rec.Text) == "" {
		return
	}
	rec.At = time.Now().UnixMilli()
	recentUtterances.mu.Lock()
	defer recentUtterances.mu.Unlock()
	recentUtterances.items = append(recentUtterances.items, rec)
	if len(recentUtterances.items) > recentUtterances.max {
		recentUtterances.items = recentUtterances.items[len(recentUtterances.items)-recentUtterances.max:]
	}
}

// clearUtterances 清空记录（管理台"清空日志"用）。
func clearUtterances() {
	recentUtterances.mu.Lock()
	recentUtterances.items = nil
	recentUtterances.mu.Unlock()
}

func listUtterances() []utteranceRecord {
	recentUtterances.mu.Lock()
	defer recentUtterances.mu.Unlock()
	out := make([]utteranceRecord, len(recentUtterances.items))
	copy(out, recentUtterances.items)
	// 新的在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
