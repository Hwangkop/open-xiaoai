package main

// 随机起播的纯函数测试：不碰网易云、不碰设备，只验证选歌规则。

import (
	"testing"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

func testSongs(n int) []netease.Song {
	songs := make([]netease.Song, 0, n)
	for i := 1; i <= n; i++ {
		songs = append(songs, netease.Song{ID: int64(i), Name: "歌" + string(rune('0'+i%10))})
	}
	return songs
}

func songIDs(songs []netease.Song) map[int64]int {
	ids := make(map[int64]int, len(songs))
	for _, s := range songs {
		ids[s.ID]++
	}
	return ids
}

func TestShuffleSongsKeepsEverySong(t *testing.T) {
	songs := testSongs(12)
	before := songIDs(songs)

	out := shuffleSongs(songs, nil)
	if len(out) != len(songs) {
		t.Fatalf("长度变了: got %d, want %d", len(out), len(songs))
	}
	after := songIDs(out)
	for id, n := range before {
		if after[id] != n {
			t.Fatalf("歌曲 %d 数量不对: got %d, want %d", id, after[id], n)
		}
	}
	// 原切片不能被就地打乱（调用方可能还要用）
	original := songIDs(songs)
	for id, n := range before {
		if original[id] != n {
			t.Fatalf("原切片被修改: 歌曲 %d 数量 %d", id, original[id])
		}
	}
}

func TestShuffleSongsAvoidsRecentFirst(t *testing.T) {
	songs := testSongs(12)
	recent := map[int64]bool{songs[0].ID: true}

	// 随机有偶然性，多试几轮：只要列表里有没放过的歌，首曲就不该是刚放过的
	for i := 0; i < 30; i++ {
		out := shuffleSongs(songs, recent)
		if recent[out[0].ID] {
			t.Fatalf("第 %d 轮首曲仍是最近播放的歌 %d", i, out[0].ID)
		}
	}
}

func TestShuffleSongsAllRecent(t *testing.T) {
	songs := testSongs(5)
	recent := map[int64]bool{}
	for _, s := range songs {
		recent[s.ID] = true
	}
	out := shuffleSongs(songs, recent)
	if len(out) != len(songs) {
		t.Fatalf("长度变了: got %d, want %d", len(out), len(songs))
	}
	if len(songIDs(out)) != len(songs) {
		t.Fatalf("全最近时丢了歌: %v", songIDs(out))
	}
}

func TestNoteRecentLocked(t *testing.T) {
	m := &musicState{}
	m.noteRecentLocked(3)
	m.noteRecentLocked(5)
	m.noteRecentLocked(3) // 重复的移到最前，不重复占位

	if len(m.recent) != 2 || m.recent[0] != 3 || m.recent[1] != 5 {
		t.Fatalf("recent = %v, want [3 5]", m.recent)
	}

	for i := 0; i < recentMusicLimit+10; i++ {
		m.noteRecentLocked(int64(100 + i))
	}
	if len(m.recent) != recentMusicLimit {
		t.Fatalf("recent 长度 = %d, want %d", len(m.recent), recentMusicLimit)
	}
	if m.recent[0] != int64(100+recentMusicLimit+9) {
		t.Fatalf("最新的歌不在最前: %v", m.recent[:3])
	}
}

func TestWantsShuffle(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"随机播放周杰伦", true},
		{"随便来点", true},
		{"打乱一下", true},
		{"shuffle", true},
		{"播放晴天", false},
		{"下一首", false},
	}
	for _, c := range cases {
		if got := wantsShuffle(c.text); got != c.want {
			t.Errorf("wantsShuffle(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestStripShuffleWords(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"随机播放周杰伦", "周杰伦"},
		{"随便来点", ""},
		{"打乱播放我喜欢的音乐", "我喜欢的音乐"},
		{"随机放点爵士", "爵士"},
	}
	for _, c := range cases {
		if got := stripShuffleWords(c.text); got != c.want {
			t.Errorf("stripShuffleWords(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
