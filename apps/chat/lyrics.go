package main

// 音乐态的第 3 道闸：这句 ASR 结果是不是"正在播的那首歌的歌词"。
//
// 为什么值得单做一层：歌词经常本身就是命令句（「留下来」「别走」「告诉我」），
// 交给模型判断必然误触发；但拿当前歌的歌词一比就是确定性的字符串问题。
// 时间轴还能再收紧——只看播放位置附近 ±12 秒的行。

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type lyricLine struct {
	At   time.Duration
	Text string
}

// lrcTimeRe 匹配 [mm:ss.xx] / [mm:ss.xxx] / [mm:ss]
var lrcTimeRe = regexp.MustCompile(`\[(\d{1,2}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// parseLRC 解析 LRC 歌词，忽略纯伴奏标记行（作词/作曲等）和空行。
func parseLRC(lrc string) []lyricLine {
	out := make([]lyricLine, 0, 64)
	for _, raw := range strings.Split(lrc, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		matches := lrcTimeRe.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}
		text := strings.TrimSpace(lrcTimeRe.ReplaceAllString(line, ""))
		if text == "" {
			continue
		}
		for _, m := range matches {
			min, _ := strconv.Atoi(m[1])
			sec, _ := strconv.Atoi(m[2])
			frac := 0
			if m[3] != "" {
				// 1 位是十分之一秒、2 位是百分之一秒、3 位是毫秒
				v, _ := strconv.Atoi(m[3])
				switch len(m[3]) {
				case 1:
					frac = v * 100
				case 2:
					frac = v * 10
				default:
					frac = v
				}
			}
			out = append(out, lyricLine{
				At:   time.Duration(min)*time.Minute + time.Duration(sec)*time.Second + time.Duration(frac)*time.Millisecond,
				Text: text,
			})
		}
	}
	return out
}

// lyricMatchWindow 认为"位置对得上"的范围。设备不报播放位置，我们用自己的起播时钟，
// TTS 打断时会累积偏差，所以窗口要比预期宽一些。
const lyricMatchWindow = 20 * time.Second

// lyricMatches 判断 text 是否像 lines 里 pos 附近的某一句歌词。
// pos < 0 表示位置未知：此时比对全部歌词行（宁可漏掉一句命令，也不要放过歌词）。
// 用拼音音节做滑窗比对：同音字（ASR 在音乐里最常见的错误）能对上，不同字的字面差异不影响。
func lyricMatches(lines []lyricLine, pos time.Duration, text string) bool {
	return lyricExplain(lines, pos, text).Matched
}

// lyricHit 歌词比对的诊断结果，管理台调试口直接展示，方便调阈值。
type lyricHit struct {
	Matched  bool   `json:"matched"`
	Line     string `json:"line,omitempty"`
	AtMs     int64  `json:"at_ms,omitempty"`
	Distance int    `json:"distance"`
	Limit    int    `json:"limit"`
	Near     bool   `json:"near"`
	Syllable int    `json:"syllables"`

	// Candidates 最像的几行，调参时看差距
	Candidates []lyricCandidate `json:"candidates,omitempty"`
}

type lyricCandidate struct {
	Line     string `json:"line"`
	AtMs     int64  `json:"at_ms"`
	Distance int    `json:"distance"`
	Near     bool   `json:"near"`
}

// lyricExplain 找出与 text 最接近的歌词行并给出判定依据。
//
// 判定规则：
//   - 位置对得上（±20s）的行走宽阈值：允许 max(1, 长度/5)+1 个音节错
//   - 位置对不上的行只认几乎完全一致（0 错），避免远方的某句歌词误杀真实指令
func lyricExplain(lines []lyricLine, pos time.Duration, text string) lyricHit {
	got := toSyllables(stripPunct(text))
	hit := lyricHit{Syllable: len(got), Distance: 99}
	if len(lines) == 0 || len(got) < 3 {
		return hit
	}

	type scored struct {
		line lyricLine
		dist int
		near bool
	}
	best := scored{dist: 99}
	var cands []scored

	for _, ln := range lines {
		want := toSyllables(stripPunct(ln.Text))
		if len(want) < 3 {
			continue
		}
		near := pos < 0 || absDuration(ln.At-pos) <= lyricMatchWindow
		if !near {
			continue // 位置对不上的行不参与判定，避免远方的歌词误杀真实指令
		}
		d := bestWindowDistance(want, got)
		cands = append(cands, scored{line: ln, dist: d, near: true})
		if d < best.dist || (d == best.dist && near && !best.near) {
			best = scored{line: ln, dist: d, near: near}
		}
	}
	if len(cands) == 0 {
		return hit
	}

	hit.Line = best.line.Text
	hit.AtMs = best.line.At.Milliseconds()
	hit.Distance = best.dist
	hit.Near = best.near

	// 位置对得上时放宽：允许约 1/3 的音节错（歌词被 ASR 听坏是常态），
	// 但 3 个音节太短，只认完全一致（这类短句基本已在白名单里拦掉）。
	limit := len(got) / 3
	if limit < 1 {
		limit = 1
	}
	if len(got) <= 3 {
		limit = 0
	}
	hit.Limit = limit
	hit.Matched = best.dist <= limit

	// 附上最像的 3 行
	sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
	n := 3
	if len(cands) < n {
		n = len(cands)
	}
	for _, c := range cands[:n] {
		hit.Candidates = append(hit.Candidates, lyricCandidate{
			Line: c.line.Text, AtMs: c.line.At.Milliseconds(), Distance: c.dist, Near: c.near,
		})
	}
	return hit
}

// bestWindowDistance 在 want 里找与 got 最接近的一段（窗口长度允许 ±1，容忍吞字/多字）。
func bestWindowDistance(want, got []string) int {
	best := 99
	for _, l := range []int{len(got) - 1, len(got), len(got) + 1} {
		if l <= 0 || l > len(want) {
			continue
		}
		for i := 0; i+l <= len(want); i++ {
			if d := syllableDistance(want[i:i+l], got); d < best {
				best = d
			}
		}
	}
	return best
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// lyricStore 按歌曲 id 缓存解析后的歌词，避免每句话都打一次接口。
type lyricStore struct {
	mu      sync.Mutex
	bySong  map[int64][]lyricLine
	failed  map[int64]time.Time
	fetcher func(ctx context.Context, id int64) (string, error)
}

func newLyricStore(fetcher func(ctx context.Context, id int64) (string, error)) *lyricStore {
	return &lyricStore{
		bySong:  make(map[int64][]lyricLine),
		failed:  make(map[int64]time.Time),
		fetcher: fetcher,
	}
}

// Lines 返回歌曲的歌词行；失败后 5 分钟内不重试，避免刷接口。
func (s *lyricStore) Lines(ctx context.Context, id int64) []lyricLine {
	if id <= 0 {
		return nil
	}
	s.mu.Lock()
	lines, ok := s.bySong[id]
	failedAt, failed := s.failed[id]
	s.mu.Unlock()
	if ok {
		return lines
	}
	if failed && time.Since(failedAt) < 5*time.Minute {
		return nil
	}
	lrc, err := s.fetcher(ctx, id)
	if err != nil || strings.TrimSpace(lrc) == "" {
		s.mu.Lock()
		s.failed[id] = time.Now()
		s.mu.Unlock()
		return nil
	}
	lines = parseLRC(lrc)
	s.mu.Lock()
	s.bySong[id] = lines
	s.mu.Unlock()
	return lines
}
