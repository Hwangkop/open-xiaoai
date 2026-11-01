package main

// 情绪标记：让模型在每句话开头标一个情绪词，TTS 按标记合成。
//
// 为什么不让用户选固定情绪：网页版那种"感情足"是跟着内容走的——开心的话上扬、
// 安慰的话放慢。固定一个 happy 只会让所有回复都笑嘻嘻。
//
// 标记形如「[开心]好呀，那就这么办～」，不会进 TTS，也不会被念出来。

import (
	"regexp"
	"strings"
)

// emotionTagMap 中文标记 → TTS 的 emotion 参数（火山大模型 TTS 支持的几种）。
var emotionTagMap = map[string]string{
	"开心": "happy", "高兴": "happy", "兴奋": "happy", "愉快": "happy",
	"难过": "sad", "伤心": "sad", "低落": "sad", "委屈": "sad",
	"生气": "angry", "恼火": "angry", "不耐烦": "angry",
	"惊讶": "surprised", "吃惊": "surprised", "惊喜": "surprised",
	"平静": "neutral", "中性": "neutral", "温和": "neutral", "温柔": "neutral",
}

// emotionTags 提示词里允许的标记词。
const emotionTags = "开心 / 难过 / 生气 / 惊讶 / 平静"

// emotionTagRe 匹配任意情绪标记（用于清除句中残留的标记）。
var emotionTagRe = regexp.MustCompile(`\[([^\[\]]{1,6})\]`)

// splitEmotionTag 取出句子开头（或任意位置）的情绪标记，返回去掉标记后的文本和 emotion。
// 认不出的标记一律删掉但不影响情绪（避免被念出来）。
func splitEmotionTag(s string) (string, string) {
	emotion := ""
	out := emotionTagRe.ReplaceAllStringFunc(s, func(m string) string {
		tag := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m, "["), "]"))
		if e, ok := emotionTagMap[tag]; ok && emotion == "" {
			emotion = e
		}
		return ""
	})
	return strings.TrimSpace(out), emotion
}

// systemPromptWithEmotion 在系统提示词末尾追加情绪标记规则。
// 只在豆包 TTS 下生效（本地 TTS 没有情绪参数，但标记会被剥掉，不影响朗读）。
func systemPromptWithEmotion(base string) string {
	return strings.TrimRight(base, "\n") + emotionRule
}

// emotionRule 追加到系统提示词末尾的规则（代码里拼，不改用户的 prompt.system）。
const emotionRule = `

【语气标记】
回复的**开头**用一个中括号情绪词标出整段该怎么说，可选：` + emotionTags + `。
例如：[开心]好呀，那咱们就这么办～
一段里有明显的情绪转折时，可以在那一句开头再标一次（比如安慰完换成轻松语气）。
标记只是给语音合成看的，不会被念出来；不要解释、不要重复标、不要一行标多个。`
