package main

// 音乐态决策树（纯函数，无副作用）。
//
// 这些规则以前散在 onUtterance 的 if 树里，改一处要读三处；现在集中在这里，
// 可以直接用表驱动测试覆盖 spec 里的每个场景（不需要模型、不需要设备）。

import "strings"

type musicPath int

const (
	musicPathNotMusic  musicPath = iota // 不在放音乐，交给调用方继续
	musicPathBareWake                   // 只喊了唤醒词
	musicPathWhitelist                  // 命中本地裸指令
	musicPathRoute                      // 交给语义路由
	musicPathDrop                       // 丢弃（噪声/歌词碎片）
)

func (p musicPath) String() string {
	switch p {
	case musicPathBareWake:
		return "bare_wake"
	case musicPathWhitelist:
		return "whitelist"
	case musicPathRoute:
		return "route"
	case musicPathDrop:
		return "drop"
	default:
		return "not_music"
	}
}

type musicInput struct {
	Matched  bool   // 这句话命中唤醒词
	WakeRest string // 剥掉唤醒词后的剩余内容
	Playing  bool   // 正在放音乐
}

type voiceDecision struct {
	Path   musicPath
	Action string // whitelist 时：next/stop/volume_up/volume_down/mute/unmute
	Text   string // route / whitelist 时实际要处理的文本
	Via    string // route 的来源：wake / verb
	Reason string // 日志与测试用
}

// decideMusic 判定音乐态下这句话走哪条路。
//
// 规则（与 spec voice-routing 一致）：
//  1. 不在放音乐 → 交给调用方
//  2. 带唤醒词：只喊唤醒词 → 应答；裸指令 → 本地执行；其余 → 路由
//  3. 不带唤醒词：裸指令 → 本地执行；带明确动作动词 → 路由；其余 → 丢弃
func decideMusic(text string, in musicInput) voiceDecision {
	if !in.Playing {
		return voiceDecision{Path: musicPathNotMusic, Reason: "not_music"}
	}
	if in.Matched {
		rest := strings.TrimSpace(in.WakeRest)
		if rest == "" {
			return voiceDecision{Path: musicPathBareWake, Reason: "bare_wake"}
		}
		if action, ok := matchMusicCommand(rest); ok {
			return voiceDecision{Path: musicPathWhitelist, Action: action, Text: rest, Reason: "wake_whitelist"}
		}
		return voiceDecision{Path: musicPathRoute, Text: rest, Via: "wake", Reason: "wake_route"}
	}
	if action, ok := matchMusicCommand(text); ok {
		return voiceDecision{Path: musicPathWhitelist, Action: action, Text: text, Reason: "whitelist"}
	}
	// 带明确动作动词（"帮换一首如愿"）说明是在指挥音箱，值得花一次路由
	if containsAnyRune(stripPunct(text), musicStrongVerbs...) {
		return voiceDecision{Path: musicPathRoute, Text: text, Via: "verb", Reason: "verb_route"}
	}
	return voiceDecision{Path: musicPathDrop, Reason: "noise"}
}
