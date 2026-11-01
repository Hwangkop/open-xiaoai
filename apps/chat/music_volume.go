package main

// 音乐态的音量/静音指令：纯本地匹配 + 本地执行，不走 AI，也不走火山 ASR。
//
// 为什么单独拎出来：音量是确定性指令，交给模型只会更慢更贵；但音乐里 ASR 容易
// 把短词听错（"大点声" → "大电声"），所以字面匹配之外再按拼音音节兜一层——
// 同音字在拼音上完全一致，能吃到这一类错误，又不会像模糊距离那样误伤相邻词。

import "log"

type musicVolumeAction int

const (
	musicVolumeNone musicVolumeAction = iota
	musicVolumeUp
	musicVolumeDown
	musicMuteOn
	musicMuteOff
)

var (
	musicVolumeUpWords   = []string{"大点声", "大声点", "大声一点", "声音大点", "声音大一点", "大一点", "音量调大", "音量加大", "音量再大点", "太小声"}
	musicVolumeDownWords = []string{"小点声", "小声点", "小声一点", "声音小点", "声音小一点", "小一点", "音量调小", "音量减小", "音量再小点", "太吵", "吵死了", "轻一点"}
	musicMuteWords       = []string{"静音", "安静", "安静点", "别出声", "闭麦", "关声音", "声音关掉"}
	musicUnmuteWords     = []string{"取消静音", "恢复音量", "解除静音", "开声音", "声音打开"}

	musicVolumeUpPhrases   = buildCommandPhrases(musicVolumeUpWords)
	musicVolumeDownPhrases = buildCommandPhrases(musicVolumeDownWords)
	musicMutePhrases       = buildCommandPhrases(musicMuteWords)
	musicUnmutePhrases     = buildCommandPhrases(musicUnmuteWords)
)

// buildCommandPhrases 把词表预编译成拼音音节序列，避免每句话重复转拼音。
func buildCommandPhrases(words []string) [][]string {
	out := make([][]string, 0, len(words))
	for _, w := range words {
		if syls := toSyllables(w); len(syls) > 0 {
			out = append(out, syls)
		}
	}
	return out
}

// musicVolumeIntent 判定音量类指令。
// 顺序讲究："取消静音"里含"静音"二字，所以必须先判取消再判静音。
func musicVolumeIntent(text string) musicVolumeAction {
	t := stripPunct(text)
	if t == "" {
		return musicVolumeNone
	}
	switch {
	case bareOrExact(t, musicUnmuteWords, musicUnmutePhrases):
		return musicMuteOff
	case bareOrExact(t, musicMuteWords, musicMutePhrases):
		return musicMuteOn
	case bareOrExact(t, musicVolumeUpWords, musicVolumeUpPhrases):
		return musicVolumeUp
	case bareOrExact(t, musicVolumeDownWords, musicVolumeDownPhrases):
		return musicVolumeDown
	}
	return musicVolumeNone
}

// bareOrExact 字面要求"基本就是这句指令"（剥掉客套词后剩 ≤1 字）；
// 拼音兜底只在整句长度接近时才算，免得歌词里的同音片段被当成指令。
func bareOrExact(text string, words []string, phrases [][]string) bool {
	for _, w := range words {
		if isBareCommand(text, w) {
			return true
		}
	}
	got := toSyllables(stripPunct(text))
	for _, want := range phrases {
		if len(want) == 0 || len(got) < len(want) || len(got) > len(want)+2 {
			continue
		}
		if syllableDistance(got, want) == 0 {
			return true
		}
	}
	return false
}

// adjustMusicVolume 执行一次音量调整（异步，避免挡住 ASR 线程）。
func (e *Engine) adjustMusicVolume(delta int) {
	before, after, err := e.speaker.AdjustMusicVolume(delta)
	if err != nil {
		log.Printf("⚠️ 调整音乐音量失败: %v", err)
		return
	}
	if before == after {
		log.Printf("🔊 音乐音量已是 %d，不再变化", after)
		return
	}
	log.Printf("🔊 音乐音量 %d → %d", before, after)
}

// setMusicMuted 执行静音/取消静音。
func (e *Engine) setMusicMuted(mute bool) {
	v, acted, err := e.speaker.SetMusicMuted(mute)
	if err != nil {
		log.Printf("⚠️ 音乐静音操作失败: %v", err)
		return
	}
	if !acted {
		if mute {
			log.Printf("🔇 音乐已经是静音状态")
		} else {
			log.Printf("🔊 音乐本来就没静音")
		}
		return
	}
	if mute {
		log.Printf("🔇 音乐已静音（原音量 %d）", v)
		return
	}
	log.Printf("🔊 取消静音，音量恢复 %d", v)
}
