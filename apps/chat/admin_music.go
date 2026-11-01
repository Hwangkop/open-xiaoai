package main

// 网易云的“搜索 / 歌单 / 下一首”状态与操作。
//
// 播放本身还是走 speaker.QueueMusic（回复播完再起播），这里只负责
// 选歌、取 VIP 链接、维护一个内存队列。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

type musicState struct {
	mu         sync.Mutex
	lastSearch []netease.Song // 最近一次搜索，供“第 N 首”使用
	queue      []netease.Song // 当前歌单队列
	idx        int            // 当前播到第几首（0-based）
	queueName  string
	current    *netease.Song
	currentURL string // 当前这首歌的播放直链（调试/AEC 参考信号用）

	// path 队列快照文件。服务重启后内存队列会丢，导致"下一首"报"没有正在播放的歌单"，
	// 所以把队列落盘，启动时读回来。
	path string
}

type musicSnapshot struct {
	Queue      []netease.Song `json:"queue"`
	Index      int            `json:"index"`
	QueueName  string         `json:"queue_name"`
	Current    *netease.Song  `json:"current"`
	CurrentURL string         `json:"current_url"`
	SavedAt    int64          `json:"saved_at"`
}

// persist 把当前队列/播放位置写到磁盘（失败只记日志，不影响播放）。
func (a *adminServer) persistMusic() {
	a.music.mu.Lock()
	snap := musicSnapshot{
		Queue:      a.music.queue,
		Index:      a.music.idx,
		QueueName:  a.music.queueName,
		Current:    a.music.current,
		CurrentURL: a.music.currentURL,
		SavedAt:    time.Now().Unix(),
	}
	path := a.music.path
	a.music.mu.Unlock()
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		log.Printf("⚠️ 保存播放队列失败: %v", err)
	}
}

// loadMusic 启动时恢复上次的队列（歌曲信息足够重新取播放地址）。
func (a *adminServer) loadMusic(path string) {
	a.music.mu.Lock()
	a.music.path = path
	a.music.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var snap musicSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return
	}
	a.music.mu.Lock()
	a.music.queue = snap.Queue
	a.music.idx = snap.Index
	a.music.queueName = snap.QueueName
	a.music.current = snap.Current
	a.music.currentURL = snap.CurrentURL
	a.music.mu.Unlock()
	log.Printf("🎵 已恢复播放队列: 《%s》第 %d/%d 首", snap.QueueName, snap.Index+1, len(snap.Queue))
}

func newMusicState() *musicState { return &musicState{} }

func (a *adminServer) searchMusic(ctx context.Context, keyword string, limit int) ([]netease.Song, error) {
	client := a.neteaseClient()
	songs, err := client.Search(ctx, keyword, limit)
	if err != nil {
		return nil, err
	}
	if len(songs) == 0 {
		return nil, fmt.Errorf("没搜到「%s」", keyword)
	}
	songs = rankSongs(keyword, songs)
	a.music.mu.Lock()
	a.music.lastSearch = songs
	a.music.mu.Unlock()
	return songs, nil
}

// playSong 取链接、排队播放，并记录为“正在播放”。
func (a *adminServer) playSong(ctx context.Context, s netease.Song) (string, error) {
	client := a.neteaseClient()
	url, err := client.SongURL(ctx, s.ID, "exhigh")
	if err != nil || url == "" {
		return "", fmt.Errorf("《%s》拿不到播放地址（可能无版权或需要会员）", s.Name)
	}
	if a.playURL == nil {
		return "", fmt.Errorf("播放器未就绪")
	}
	if err := a.playURL(url); err != nil {
		return "", err
	}
	a.music.mu.Lock()
	cur := s
	a.music.current = &cur
	a.music.currentURL = url
	a.music.mu.Unlock()
	a.persistMusic()
	log.Printf("🎵 播放: %s - %s", s.Name, s.Artists)

	// 平时是"排进队列，等 AI 回复播完再起播"（避免和确认语重叠）；
	// 但音乐态路由没有回复，没人来起播 → 这里必须自己放。
	if a.speaker.ImmediatePlayActive() {
		a.speaker.PlayPendingMusic()
	}
	return fmt.Sprintf("正在播放《%s》- %s", s.Name, s.Artists), nil
}

// playFromList 从 start 开始找第一首能播的。
func (a *adminServer) playFromList(ctx context.Context, songs []netease.Song, start int) (netease.Song, error) {
	if start < 0 {
		start = 0
	}
	var lastErr error
	for i := start; i < len(songs); i++ {
		if _, err := a.playSong(ctx, songs[i]); err == nil {
			a.music.mu.Lock()
			a.music.idx = i
			a.music.mu.Unlock()
			a.persistMusic()
			return songs[i], nil
		} else {
			lastErr = err
			log.Printf("⏭️ 跳过《%s》: %v", songs[i].Name, err)
		}
	}
	if lastErr != nil {
		return netease.Song{}, fmt.Errorf("这个列表里没有能播的歌（最后一首：%v）", lastErr)
	}
	return netease.Song{}, fmt.Errorf("列表是空的")
}

// searchAndPlay 直接点歌（保持原来的行为）。
func (a *adminServer) searchAndPlay(ctx context.Context, query string) (string, error) {
	songs, err := a.searchMusic(ctx, query, 15)
	if err != nil {
		return "", err
	}
	// 点歌单曲时不把搜索结果当队列，清掉旧队列
	a.music.mu.Lock()
	a.music.queue = nil
	a.music.queueName = ""
	a.music.mu.Unlock()
	s, err := a.playFromList(ctx, songs, 0)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("正在播放《%s》- %s", s.Name, s.Artists), nil
}

// playSearchIndex 播放最近一次搜索结果的第 index 首（1-based）。
func (a *adminServer) playSearchIndex(ctx context.Context, index int) (string, error) {
	a.music.mu.Lock()
	songs := append([]netease.Song(nil), a.music.lastSearch...)
	a.music.mu.Unlock()
	if len(songs) == 0 {
		return "", fmt.Errorf("还没有搜索结果，先 search_music")
	}
	if index < 1 || index > len(songs) {
		return "", fmt.Errorf("序号超出范围（1-%d）", len(songs))
	}
	msg, err := a.playSong(ctx, songs[index-1])
	if err != nil {
		return "", err
	}
	return msg, nil
}

// playNamedPlaylist 按名字找歌单（含“我喜欢”），播放第一首能播的。
func (a *adminServer) playNamedPlaylist(ctx context.Context, name string) (string, error) {
	client := a.neteaseClient()
	acc, err := client.GetAccount(ctx)
	if err != nil || acc == nil || !acc.LoggedIn {
		return "", fmt.Errorf("网易云未登录，请先在应用管理里扫码")
	}
	lists, err := client.UserPlaylists(ctx, acc.UserID)
	if err != nil {
		return "", fmt.Errorf("读取歌单失败: %w", err)
	}
	pl, err := matchPlaylist(lists, name)
	if err != nil {
		return "", err
	}
	songs, err := client.PlaylistDetail(ctx, pl.ID)
	if err != nil {
		return "", fmt.Errorf("读取歌单详情失败: %w", err)
	}
	if len(songs) == 0 {
		return "", fmt.Errorf("歌单「%s」是空的", pl.Name)
	}
	a.music.mu.Lock()
	a.music.queue = songs
	a.music.queueName = pl.Name
	a.music.idx = 0
	a.music.mu.Unlock()
	s, err := a.playFromList(ctx, songs, 0)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("开始播放歌单《%s》，第一首《%s》- %s", pl.Name, s.Name, s.Artists), nil
}

// playDaily 播放网易云「每日推荐」。
func (a *adminServer) playDaily(ctx context.Context) (string, error) {
	client := a.neteaseClient()
	songs, err := client.DailySongs(ctx)
	if err != nil {
		return "", err
	}
	if len(songs) == 0 {
		return "", fmt.Errorf("今天的每日推荐是空的")
	}
	a.music.mu.Lock()
	a.music.queue = songs
	a.music.queueName = "每日推荐"
	a.music.idx = 0
	a.music.mu.Unlock()
	s, err := a.playFromList(ctx, songs, 0)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("今天的每日推荐共 %d 首，先放《%s》- %s", len(songs), s.Name, s.Artists), nil
}

// nextMusic 播放队列的下一首。
func (a *adminServer) nextMusic(ctx context.Context) (string, error) {
	a.music.mu.Lock()
	q := a.music.queue
	idx := a.music.idx
	name := a.music.queueName
	a.music.mu.Unlock()
	if len(q) == 0 {
		return "", fmt.Errorf("没有正在播放的歌单")
	}
	if idx+1 >= len(q) {
		return "", fmt.Errorf("歌单《%s》已经放到最后一首了", name)
	}
	s, err := a.playFromList(ctx, q, idx+1)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("下一首：《%s》- %s", s.Name, s.Artists), nil
}

// matchPlaylist 按名字模糊匹配；“我喜欢/收藏”对应 specialType=5。
func matchPlaylist(lists []netease.Playlist, name string) (netease.Playlist, error) {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, " ", "")
		s = strings.Trim(s, "《》「」\"'")
		return s
	}
	target := norm(name)
	if target == "" || strings.Contains(target, "喜欢") || strings.Contains(target, "收藏") {
		for _, p := range lists {
			if p.SpecialType == 5 {
				return p, nil
			}
		}
	}
	var exact, fuzzy []netease.Playlist
	for _, p := range lists {
		pn := norm(p.Name)
		if pn == target {
			exact = append(exact, p)
		} else if strings.Contains(pn, target) || strings.Contains(target, pn) {
			fuzzy = append(fuzzy, p)
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = fuzzy
	}
	if len(pick) == 1 {
		return pick[0], nil
	}
	if len(pick) > 1 {
		names := make([]string, 0, len(pick))
		for _, p := range pick {
			names = append(names, p.Name)
		}
		return netease.Playlist{}, fmt.Errorf("找到多个歌单：%s", strings.Join(names, "、"))
	}
	// 没匹配上时给个列表，方便模型追问
	names := make([]string, 0, len(lists))
	for _, p := range lists {
		names = append(names, p.Name)
	}
	return netease.Playlist{}, fmt.Errorf("没有叫「%s」的歌单，现有：%s", name, strings.Join(names, "、"))
}

// musicDesc 给路由 prompt 提供上下文（当前歌 + 音量）。
func (a *adminServer) musicDesc() (song, artists string, volume int) {
	a.music.mu.Lock()
	cur := a.music.current
	a.music.mu.Unlock()
	if cur != nil {
		song, artists = cur.Name, cur.Artists
	}
	volume, _ = a.speaker.deviceVolume()
	return
}
