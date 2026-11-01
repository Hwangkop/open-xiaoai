package main

import "testing"

func TestSplitEmotionTag(t *testing.T) {
	cases := []struct {
		in, text, emotion string
	}{
		{"[开心]好呀，那就这么办～", "好呀，那就这么办～", "happy"},
		{"[平静]明天九点开始", "明天九点开始", "neutral"},
		{"[难过]没关系的，慢慢来", "没关系的，慢慢来", "sad"},
		{"没有标记的一句话", "没有标记的一句话", ""},
		{"[未知]内容", "内容", ""}, // 未知标记也剥掉，别念出来
		{"句中[生气]出现标记", "句中出现标记", "angry"},
		{"[开心][平静]两个标记", "两个标记", "happy"}, // 以第一个为准
	}
	for _, c := range cases {
		text, emo := splitEmotionTag(c.in)
		if text != c.text || emo != c.emotion {
			t.Errorf("splitEmotionTag(%q) = (%q,%q), want (%q,%q)", c.in, text, emo, c.text, c.emotion)
		}
	}
}
