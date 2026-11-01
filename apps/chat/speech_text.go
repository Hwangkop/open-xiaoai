package main

import "strings"

// trimForSpeech 去掉句首句尾的标点、引号与空白。
// 流式断句经常把单独一个引号（“ ” “ ”）留在句首，直接送 TTS 会念出奇怪的气口。
func trimForSpeech(s string) string {
	const cut = " \t\r\n\u201c\u201d\u2018\u2019\"'，。！？、,.!?~～…—-·:：;；"
	return strings.Trim(s, cut)
}
