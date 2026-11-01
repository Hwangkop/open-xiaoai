package main

// 音乐态路由漏斗的观测数据：每句话走到哪一层、路由器判成什么、置信度多少。
// 环形缓冲（最近 200 条），管理台「唤醒与识别」页直接展示，用来调 prompt 和阈值。

import (
	"sync"
	"time"
)

type routeEntry struct {
	At         int64   `json:"at"`                   // unix 毫秒
	Text       string  `json:"text"`                 // ASR 原文
	Stage      string  `json:"stage"`                // whitelist / router / dropped
	Via        string  `json:"via,omitempty"`        // wake / none（只有路由那层有）
	Action     string  `json:"action,omitempty"`     // 白名单动作 或 路由 intent
	Query      string  `json:"query,omitempty"`      // play 意图里的搜索词
	Confidence float64 `json:"confidence,omitempty"` // 路由置信度
	Result     string  `json:"result,omitempty"`     // executed / dialog / ignored / low_confidence / error
	LatencyMs  int64   `json:"latency_ms,omitempty"`
}

var routeLog = struct {
	mu     sync.Mutex
	items  []routeEntry
	max    int
	total  int
	noise  int // 只计数不出现在列表里的噪声（没唤醒词被丢的歌词碎片）
	stage  map[string]int
	intent map[string]int
}{
	max:    200,
	stage:  make(map[string]int),
	intent: make(map[string]int),
}

func recordRoute(e routeEntry) {
	e.At = time.Now().UnixMilli()
	routeLog.mu.Lock()
	defer routeLog.mu.Unlock()
	routeLog.items = append(routeLog.items, e)
	if len(routeLog.items) > routeLog.max {
		routeLog.items = routeLog.items[len(routeLog.items)-routeLog.max:]
	}
	routeLog.total++
	routeLog.stage[e.Stage]++
	if e.Stage == "router" && e.Action != "" {
		routeLog.intent[e.Action]++
	}
}

// recordNoiseDrop 只记数字不记明细：音乐里的歌词碎片每几秒一条，
// 全塞进列表会把真正有用的白名单/路由记录淹掉。
func recordNoiseDrop() {
	routeLog.mu.Lock()
	defer routeLog.mu.Unlock()
	routeLog.total++
	routeLog.noise++
	routeLog.stage["dropped"]++
}

func routeLogSnapshot() (items []routeEntry, stats map[string]any) {
	routeLog.mu.Lock()
	defer routeLog.mu.Unlock()
	out := make([]routeEntry, len(routeLog.items))
	copy(out, routeLog.items)

	stage := make(map[string]int, len(routeLog.stage))
	for k, v := range routeLog.stage {
		stage[k] = v
	}
	intent := make(map[string]int, len(routeLog.intent))
	for k, v := range routeLog.intent {
		intent[k] = v
	}
	return out, map[string]any{
		"total":  routeLog.total,
		"noise":  routeLog.noise,
		"stage":  stage,
		"intent": intent,
	}
}

func clearRouteLog() {
	routeLog.mu.Lock()
	defer routeLog.mu.Unlock()
	routeLog.items = nil
	routeLog.total = 0
	routeLog.noise = 0
	routeLog.stage = make(map[string]int)
	routeLog.intent = make(map[string]int)
}
