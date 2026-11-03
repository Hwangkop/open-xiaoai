package main

// 网易云的“搜索 / 歌单 / 下一首”状态与操作。
//
// 音乐工具直接下发播放；起播指令成功后统一提交当前歌曲、队列和序号。
// agent 与自动续播共用这份状态，不依赖对话里的记忆。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cxjava/open-xiaoai/pkg/netease"
)

type musicState struct {
	opMu               sync.Mutex // 串行化选歌、自动续播与停止，避免多个来源同时推进队列。
	mu                 sync.Mutex
	lastSearch         []netease.Song // 最近一次搜索，供“第 N 首”使用
	queue              []netease.Song // 持续播放的歌单，搜索点播不能覆盖。
	idx                int            // 原歌单最后播到的位置（0-based），点播期间保持不变。
	queueName          string
	lastError          string
	current            *netease.Song
	oneShot            bool   // 当前为单次点播；播完/下一首时回到原歌单的下一首。
	currentURL         string // 当前这首歌的播放直链（调试/AEC 参考信号用）
	bilibili           *bilibiliQueue
	bilibiliCancel     context.CancelFunc
	bilibiliGeneration uint64
	recent             []int64 // 最近播放过的歌（最新在前），随机起播时避开

	// path 队列快照文件。服务重启后内存队列会丢，导致"下一首"报"没有正在播放的歌单"，
	// 所以把队列落盘，启动时读回来。
	path string
}

type musicSnapshot struct {
	Queue      []netease.Song `json:"queue"`
	Index      int            `json:"index"`
	QueueName  string         `json:"queue_name"`
	Current    *netease.Song  `json:"current"`
	OneShot    bool           `json:"one_shot,omitempty"`
	CurrentURL string         `json:"current_url"`
	Recent     []int64        `json:"recent"`
	SavedAt    int64          `json:"saved_at"`
	Bilibili   *bilibiliQueue `json:"bilibili,omitempty"`
}

// persist 把当前队列/播放位置写到磁盘（失败只记日志，不影响播放）。
func (a *adminServer) persistMusic() {
	a.music.mu.Lock()
	snap := musicSnapshot{
		Queue:      a.music.queue,
		Index:      a.music.idx,
		QueueName:  a.music.queueName,
		Current:    a.music.current,
		OneShot:    a.music.oneShot,
		CurrentURL: a.music.currentURL,
		Recent:     a.music.recent,
		SavedAt:    time.Now().Unix(),
	}
	if a.music.bilibili != nil {
		copyQ := *a.music.bilibili
		copyQ.Items = append([]bilibiliCatalogItem(nil), copyQ.Items...)
		snap.Bilibili = &copyQ
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
	if err := writeFileAtomic(path, raw); err != nil {
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
	// 旧版本把搜索候选持久化为歌单；只能保留当时选中的歌曲，不能继续放候选。
	if strings.HasPrefix(snap.QueueName, "搜索：") || snap.QueueName == "搜索候选" {
		snap.OneShot = snap.Current != nil
		snap.Queue, snap.QueueName, snap.Index = nil, "", -1
	}
	a.music.mu.Lock()
	a.music.queue = snap.Queue
	a.music.idx = snap.Index
	a.music.queueName = snap.QueueName
	a.music.current = snap.Current
	a.music.oneShot = snap.OneShot
	a.music.currentURL = snap.CurrentURL
	a.music.recent = snap.Recent
	if q := snap.Bilibili; q != nil && len(q.Items) > 0 && q.Index >= 0 && q.Index < len(q.Items) {
		// 不在启动时自行下载或出声；用户可明确续播。
		if q.Status == "preparing" {
			q.Status = "paused"
		}
		a.music.bilibili = q
	}
	a.music.mu.Unlock()
	log.Printf("🎵 已恢复播放队列: 《%s》第 %d/%d 首", snap.QueueName, snap.Index+1, len(snap.Queue))
}

func newMusicState() *musicState { return &musicState{idx: -1} }

// recentMusicLimit 记住最近播放的歌曲数。列表本身不长时（每日推荐 20~32 首），
// 随机起播很容易连着撞上刚放过的歌，靠这份记忆把重复压下去。
const recentMusicLimit = 50

// noteRecentLocked 把一首歌记到最近播放列表最前面（去重、截断）。
func (m *musicState) noteRecentLocked(id int64) {
	if id == 0 {
		return
	}
	ids := make([]int64, 0, len(m.recent)+1)
	ids = append(ids, id)
	for _, v := range m.recent {
		if v != id {
			ids = append(ids, v)
		}
	}
	if len(ids) > recentMusicLimit {
		ids = ids[:recentMusicLimit]
	}
	m.recent = ids
}

// recentSetLocked 返回最近播放歌曲的集合（调用方需持有锁）。
func (m *musicState) recentSetLocked() map[int64]bool {
	set := make(map[int64]bool, len(m.recent))
	for _, id := range m.recent {
		set[id] = true
	}
	return set
}

// shuffleSongs 返回打乱后的副本，并保证第一首尽量不是最近刚放过的歌。
// 随机起播的意义就在于“每次不一样”；如果随机出来还是刚放的那首，听感上和没随机一样。
func shuffleSongs(songs []netease.Song, recent map[int64]bool) []netease.Song {
	out := append([]netease.Song(nil), songs...)
	rand.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	if len(out) > 1 && recent[out[0].ID] {
		for i := 1; i < len(out); i++ {
			if !recent[out[i].ID] {
				out[0], out[i] = out[i], out[0]
				break
			}
		}
	}
	return out
}

// shuffleWords 用户表达“随机”时常见的说法。
var shuffleWords = []string{"随机", "随便", "打乱", "shuffle"}

// wantsShuffle 判断一句话里有没有“随机/打乱”的意思（分类器兜底路径也用它）。
func wantsShuffle(text string) bool {
	t := strings.ToLower(text)
	for _, w := range shuffleWords {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// stripShuffleWords 去掉“随机播放”这类修饰，剩下真正的点歌关键词。
func stripShuffleWords(text string) string {
	t := strings.ToLower(text)
	for _, w := range []string{
		"随机播放", "随机放点", "随机放", "随机来点", "随机来", "随机",
		"打乱播放", "打乱", "shuffle",
		"随便播放", "随便放点", "随便放", "随便来点", "随便来", "随便",
		"播放", "放点", "来点",
	} {
		t = strings.ReplaceAll(t, w, "")
	}
	return strings.TrimSpace(t)
}

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

// playSong 直接下发播放，成功后一次性提交队列、序号与歌曲，避免快照记录错位。
// 调用方必须持有 music.opMu。
func (a *adminServer) playSong(ctx context.Context, songs []netease.Song, index int, name string) (string, error) {
	s := songs[index]
	url, err := a.startMusicSong(ctx, s)
	if err != nil {
		return "", err
	}
	a.music.mu.Lock()
	a.music.queue = append([]netease.Song(nil), songs...)
	a.music.idx, a.music.queueName, a.music.oneShot = index, name, false
	a.recordPlayingSongLocked(s, url)
	a.music.mu.Unlock()
	a.finishMusicSelection(s)
	return fmt.Sprintf("正在播放《%s》- %s", s.Name, s.Artists), nil
}

// startMusicSong 只取链接并下发；失败时不改变歌单或点播状态。
func (a *adminServer) startMusicSong(ctx context.Context, s netease.Song) (string, error) {
	client := a.neteaseClient()
	url, err := client.SongURL(ctx, s.ID, "exhigh")
	if err != nil || url == "" {
		return "", fmt.Errorf("《%s》拿不到播放地址（可能无版权或需要会员）", s.Name)
	}
	if a.playURL == nil {
		return "", fmt.Errorf("播放器未就绪")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := a.playURL(url); err != nil {
		return "", err
	}
	return url, nil
}

func (a *adminServer) recordPlayingSongLocked(s netease.Song, url string) {
	a.music.cancelBilibiliLocked(true)
	cur := s
	a.music.current = &cur
	a.music.lastError = ""
	a.music.currentURL = url
	a.music.noteRecentLocked(s.ID)
}

func (a *adminServer) finishMusicSelection(s netease.Song) {
	a.speaker.setMusicDuration(s.Duration)
	a.persistMusic()
	log.Printf("🎵 播放: %s - %s", s.Name, s.Artists)
}

// playOneShot 单次点播替换当前声音，持续歌单及序号保持不变；调用方持有 opMu。
func (a *adminServer) playOneShot(ctx context.Context, s netease.Song) (string, error) {
	url, err := a.startMusicSong(ctx, s)
	if err != nil {
		return "", err
	}
	a.music.mu.Lock()
	a.music.oneShot = true
	a.recordPlayingSongLocked(s, url)
	name, next, total := a.music.queueName, a.music.idx+1, len(a.music.queue)
	a.music.mu.Unlock()
	a.finishMusicSelection(s)
	message := fmt.Sprintf("单次点播《%s》- %s", s.Name, s.Artists)
	if next < total {
		message += fmt.Sprintf("，播完继续歌单《%s》第 %d 首", name, next+1)
	}
	return message, nil
}

// playFromList 从 start 开始找第一首能播的。
func (a *adminServer) playFromList(ctx context.Context, songs []netease.Song, start int, name string) (netease.Song, error) {
	if start < 0 {
		start = 0
	}
	var lastErr error
	for i := start; i < len(songs); i++ {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		if _, err := a.playSong(ctx, songs, i, name); err == nil {
			return songs[i], nil
		} else {
			lastErr = err
			log.Printf("⏭️ 跳过《%s》: %v", songs[i].Name, err)
		}
	}
	if lastErr != nil {
		a.music.mu.Lock()
		a.music.lastError = "这个列表暂时没有能播放的歌曲，请重试或换个歌单"
		a.music.mu.Unlock()
		return netease.Song{}, fmt.Errorf("这个列表里没有能播的歌（最后一首：%v）", lastErr)
	}
	return netease.Song{}, fmt.Errorf("列表是空的")
}

// searchAndPlay 搜索后单次点播一首；shuffle 只影响候选选择，不会创建持续歌单。
func (a *adminServer) searchAndPlay(ctx context.Context, query string, shuffle bool) (string, error) {
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	if shuffle {
		if q := stripShuffleWords(query); q != "" {
			query = q
		} else {
			// “随机播放”没给关键词：当作随便放点，走每日推荐
			return a.playDailyLocked(ctx)
		}
	}
	limit := 15
	if shuffle {
		limit = 30 // 随机挑歌时多取一些，选择面更大
	}
	songs, err := a.searchMusic(ctx, query, limit)
	if err != nil {
		return "", err
	}
	a.music.mu.Lock()
	if shuffle {
		songs = shuffleSongs(songs, a.music.recentSetLocked())
		log.Printf("🎲 随机点歌 %q：候选 %d 首", query, len(songs))
	}
	a.music.mu.Unlock()
	var lastErr error
	for _, song := range songs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		message, err := a.playOneShot(ctx, song)
		if err == nil {
			return message, nil
		}
		lastErr = err
		log.Printf("⏭️ 点播候选《%s》不可播放: %v", song.Name, err)
	}
	return "", fmt.Errorf("搜索候选暂时无法播放: %v", lastErr)
}

// playSearchIndex 播放最近一次搜索结果的第 index 首（1-based）。
func (a *adminServer) playSearchIndex(ctx context.Context, index int) (string, error) {
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	a.music.mu.Lock()
	songs := append([]netease.Song(nil), a.music.lastSearch...)
	a.music.mu.Unlock()
	if len(songs) == 0 {
		return "", fmt.Errorf("还没有搜索结果，先 search_music")
	}
	if index < 1 || index > len(songs) {
		return "", fmt.Errorf("序号超出范围（1-%d）", len(songs))
	}
	return a.playOneShot(ctx, songs[index-1])
}

// playNamedPlaylist 按名字找歌单（含“我喜欢”）。
// 默认随机播放：每次都从第一首开始既不智能，也容易让人以为指令没生效。
func (a *adminServer) playNamedPlaylist(ctx context.Context, name string, shuffle bool) (string, error) {
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
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
	if shuffle {
		songs = shuffleSongs(songs, a.music.recentSetLocked())
		log.Printf("🎲 随机播放歌单《%s》：%d 首", pl.Name, len(songs))
	}
	a.music.mu.Unlock()
	s, err := a.playFromList(ctx, songs, 0, pl.Name)
	if err != nil {
		return "", err
	}
	if shuffle {
		return fmt.Sprintf("正在播放歌单《%s》，随机先放《%s》- %s", pl.Name, s.Name, s.Artists), nil
	}
	return fmt.Sprintf("开始播放歌单《%s》，第一首《%s》- %s", pl.Name, s.Name, s.Artists), nil
}

// playDaily 播放网易云「每日推荐」。推荐本来就是拿来随便听的，
// 每次固定从第一首开始等于把“推荐”变成了“单曲循环第一首”。
func (a *adminServer) playDaily(ctx context.Context) (string, error) {
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	return a.playDailyLocked(ctx)
}

func (a *adminServer) playDailyLocked(ctx context.Context) (string, error) {
	client := a.neteaseClient()
	songs, err := client.DailySongs(ctx)
	if err != nil {
		return "", err
	}
	if len(songs) == 0 {
		return "", fmt.Errorf("今天的每日推荐是空的")
	}
	a.music.mu.Lock()
	songs = shuffleSongs(songs, a.music.recentSetLocked())
	log.Printf("🎲 每日推荐随机起播：%d 首", len(songs))
	a.music.mu.Unlock()
	s, err := a.playFromList(ctx, songs, 0, "每日推荐")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("今天的每日推荐共 %d 首，正在播放《%s》- %s", len(songs), s.Name, s.Artists), nil
}

// nextMusic 播放队列的下一首。
func (a *adminServer) nextMusic(ctx context.Context) (string, error) {
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	return a.nextMusicLocked(ctx)
}

func (a *adminServer) nextMusicLocked(ctx context.Context) (string, error) {
	if handled, msg, err := a.nextBilibiliLocked(); handled {
		return msg, err
	}
	a.music.mu.Lock()
	q := a.music.queue
	idx := a.music.idx
	name := a.music.queueName
	oneShot := a.music.oneShot
	a.music.mu.Unlock()
	if len(q) == 0 {
		return "", fmt.Errorf("没有持续播放的歌单，单次点播结束后停止")
	}
	if idx+1 >= len(q) {
		return "", fmt.Errorf("歌单《%s》已经放到最后一首了", name)
	}
	s, err := a.playFromList(ctx, q, idx+1, name)
	if err != nil {
		return "", err
	}
	if oneShot {
		return fmt.Sprintf("回到歌单《%s》：《%s》- %s", name, s.Name, s.Artists), nil
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
	if a.audiobooks != nil {
		book := a.audiobooks.status()
		if book.Status == "playing" || book.Status == "interrupted" || book.Status == "paused" {
			volume, _ = a.speaker.deviceVolume()
			return "听书：《" + book.Title + "》/" + book.ChapterTitle, "", volume
		}
	}
	a.music.mu.Lock()
	cur := a.music.current
	if q := a.music.bilibili; q != nil {
		item := q.Items[q.Index]
		a.music.mu.Unlock()
		volume, _ = a.speaker.deviceVolume()
		return item.Title, item.Author, volume
	}
	a.music.mu.Unlock()
	if cur != nil {
		song, artists = cur.Name, cur.Artists
	}
	volume, _ = a.speaker.deviceVolume()
	return
}
