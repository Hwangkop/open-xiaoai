package main

// 唤醒词的拼音匹配。
//
// 比手写字表更强的地方：ASR 常把「豆包」听成「多吧 / 豆康 / 豆宝」——
// 这些字的字形完全不同，但拼音只差一个音节。按拼音算编辑距离就能覆盖。
//
// 注意：如果 ASR 听成完全不相干的音节（比如「德当」），拼音也救不回来，
// 那是信噪比问题，不是匹配问题。

import (
	"strings"

	"github.com/mozillazg/go-pinyin"
)

// 四音节目标：只要第 1、3 音节对得上、整体错不超过 2 个就算命中。
// 这样「豆康豆吧」能过，「对吧对方」（dui）过不了。
var wakeQuadTargets = [][]string{
	{"dou", "bao", "dou", "bao"},
	{"duo", "bao", "duo", "bao"},
	{"tao", "bao", "tao", "bao"}, // 豆包→淘宝（实测高频）
	{"du", "ba", "du", "ba"},     // 豆包→读吧
	{"tu", "ba", "tu", "ba"},     // 豆包→图吧
	{"du", "a", "du", "a"},       // 豆包→杜啊
	{"du", "bo", "du", "bo"},     // 豆包→杜博
	{"de", "bao", "de", "bao"},   // 豆包→的包
	{"xiao", "du", "xiao", "du"},
	{"xiao", "dou", "xiao", "dou"},
}

// 双音节目标：要求完全一致的拼音（避免「多少钱 duo shao」「豆沙包 dou sha」误中）。
var wakeDuoTargets = [][]string{
	{"dou", "bao"}, {"duo", "bao"}, {"dou", "ba"}, {"duo", "ba"},
	{"tao", "bao"}, {"du", "ba"}, {"tu", "ba"},
	{"du", "bo"}, {"de", "bao"}, {"di", "bao"}, {"dao", "bao"}, {"de", "ba"},
	{"de", "bang"}, {"du", "bang"}, {"zen", "bao"}, {"dao", "dang"},
	{"xiao", "du"}, {"xiao", "dou"},
}

// toSyllables 把中文文本转成无声调拼音音节（非汉字原样保留）。
func toSyllables(text string) []string {
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal // 无声调
	args.Heteronym = false
	rows := pinyin.Pinyin(text, args)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		syl := strings.ToLower(row[0])
		// 只要纯字母音节，标点/数字直接跳过
		if syl == "" || strings.ContainsAny(syl, "，。！？、,.!?~～…—：:;；\"' ") {
			continue
		}
		out = append(out, syl)
	}
	return out
}

// syllableDistance 音节级编辑距离。
func syllableDistance(a, b []string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func minInt(vs ...int) int {
	m := vs[0]
	for _, v := range vs[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

// matchWakePinyin 判断文本开头是否像唤醒词。
func matchWakePinyin(text string) bool {
	syls := toSyllables(text)
	if len(syls) < 2 {
		return false
	}
	// 四音节：结构（第 1、3 音节）+ 距离 ≤2
	if len(syls) >= 4 {
		for _, t := range wakeQuadTargets {
			if syls[0] != t[0] || syls[2] != t[2] {
				continue
			}
			if syllableDistance(syls[:4], t) <= 2 {
				return true
			}
		}
	}
	// 双音节：拼音完全一致
	for _, t := range wakeDuoTargets {
		if syls[0] == t[0] && syls[1] == t[1] {
			return true
		}
	}
	return false
}
