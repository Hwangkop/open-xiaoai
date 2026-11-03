package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type AudiobookConfig struct {
	Enabled        bool   `yaml:"enabled"`
	CacheDir       string `yaml:"cache_dir"`
	ChunkChars     int    `yaml:"chunk_chars"`
	Prefetch       int    `yaml:"prefetch"`
	SynthesisGapMS int    `yaml:"synthesis_gap_ms"`
}

func (c AudiobookConfig) normalized() AudiobookConfig {
	if c.ChunkChars < 80 || c.ChunkChars > 400 {
		c.ChunkChars = 180
	}
	if c.Prefetch < 1 || c.Prefetch > 3 {
		c.Prefetch = 2
	}
	if c.SynthesisGapMS < 500 {
		c.SynthesisGapMS = 1000
	}
	return c
}

type audiobookBookmark struct {
	BookID      string `json:"book_id"`
	Version     string `json:"version"`
	ChunkHash   string `json:"chunk_hash"`
	ChapterID   string `json:"chapter_id"`
	Part        int    `json:"part"`
	ChunkChars  int    `json:"chunk_chars"`
	TextVersion string `json:"text_version"`
	Completed   bool   `json:"completed"`
	UpdatedAt   string `json:"updated_at"`
}
type audiobookAudio struct {
	Name       string
	DurationMS int
}
type audiobookSession struct {
	book     audiobookBook
	chunks   []audiobookChunk
	cursor   int
	audio    map[int]audiobookAudio
	tts      *DoubaoTTS
	ctx      context.Context
	cancel   context.CancelFunc
	status   string
	url      string
	err      string
	failures int
	retryAt  time.Time
}
type audiobookStatus struct {
	Enabled       bool                         `json:"enabled"`
	Status        string                       `json:"status"`
	BookID        string                       `json:"book_id,omitempty"`
	Title         string                       `json:"title,omitempty"`
	ChapterID     string                       `json:"chapter_id,omitempty"`
	ChapterTitle  string                       `json:"chapter_title,omitempty"`
	Part          int                          `json:"part"`
	Segment       int                          `json:"segment"`
	TotalSegments int                          `json:"total_segments"`
	Buffered      int                          `json:"buffered"`
	Error         string                       `json:"error,omitempty"`
	Bookmarks     map[string]audiobookBookmark `json:"bookmarks"`
}

// mediaMu 与音乐选歌共用：自动续段、用户切书、切歌都按这个顺序串行化。
// 锁顺序只能 mediaMu → mu；合成在锁外执行，且全服务最多一个在途请求。
type audiobookService struct {
	mu            sync.Mutex
	mediaMu       *sync.Mutex
	cfg           AudiobookConfig
	dir           string
	knowledge     *knowledgeService
	session       *audiobookSession
	bookmarks     map[string]audiobookBookmark
	dialogTurn    uint64
	synthesizing  bool
	nextSynthesis time.Time
	ready         bool
	initialURL    string
	once          sync.Once
	ttsConfig     func() TTSConfig
	baseURL       func() string
	busy          func() bool
	play          func(string, int) error
	pause         func() error
	resume        func() error
	stop          func()
	mediaURL      func() string
	mediaPlaying  func() bool
	onSelect      func() // 已验证的选书操作撤销其他来源尚未完成的音轨准备。
	synthesize    func(context.Context, *DoubaoTTS, string) (audiobookAudio, error)
}

func newAudiobookService(configPath string, cfg AudiobookConfig, k *knowledgeService, mediaMu *sync.Mutex) (*audiobookService, error) {
	cfg = cfg.normalized()
	dir := strings.TrimSpace(cfg.CacheDir)
	if dir == "" {
		dir = "audiobooks"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(configPath), dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "audio"), 0o755); err != nil {
		return nil, err
	}
	b := &audiobookService{cfg: cfg, dir: dir, knowledge: k, mediaMu: mediaMu, bookmarks: map[string]audiobookBookmark{}}
	data, err := os.ReadFile(filepath.Join(dir, "progress.json"))
	if err == nil {
		if err := json.Unmarshal(data, &b.bookmarks); err != nil {
			return nil, fmt.Errorf("听书进度文件损坏，请先恢复备份：%w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if b.bookmarks == nil {
		b.bookmarks = map[string]audiobookBookmark{}
	}
	var playback struct {
		URL string `json:"url"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "playback.json")); err == nil {
		if err := json.Unmarshal(data, &playback); err != nil {
			return nil, fmt.Errorf("听书播放记录损坏：%w", err)
		}
		if strings.Contains(playback.URL, "/audiobooks/audio/") {
			b.initialURL = playback.URL
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b.synthesize = func(ctx context.Context, t *DoubaoTTS, text string) (audiobookAudio, error) {
		name, err := t.Synthesize(ctx, text)
		if err != nil {
			return audiobookAudio{}, err
		}
		duration, err := audiobookWAVDuration(filepath.Join(t.Dir(), name))
		if err != nil {
			_ = os.Remove(filepath.Join(t.Dir(), name))
			return audiobookAudio{}, err
		}
		return audiobookAudio{name, duration}, nil
	}
	return b, nil
}

func (b *audiobookService) start(ctx context.Context) {
	b.once.Do(func() {
		go func() {
			// 服务重启不自动出声。首次连接后清理上次遗留的听书播放，保留书签供明确续听。
			ticker := time.NewTicker(300 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					b.close()
					return
				case <-ticker.C:
					b.step()
				}
			}
		}()
	})
}
func (b *audiobookService) close() {
	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.session; s != nil {
		s.cancel()
	}
}

func (b *audiobookService) catalog() ([]audiobookBook, error) { return b.knowledge.audiobookBooks() }
func (b *audiobookService) request(query, chapterID string) (string, error) {
	books, err := b.catalog()
	if err != nil {
		return "", err
	}
	book, err := findAudiobook(books, query)
	if err != nil {
		return "", err
	}
	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	start, part := 0, 0
	if chapterID != "" {
		found := false
		for i, c := range book.Chapters {
			if c.ID == chapterID {
				start = i
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("章节不存在，请先列出这本书的章节再选择 chapter_id")
		}
	} else if mark, ok := b.bookmarks[book.ID]; ok && !mark.Completed {
		if mark.Version != book.Version || mark.ChunkChars != b.cfg.ChunkChars || mark.TextVersion != audiobookTextVersion {
			return "", fmt.Errorf("书籍版本或分段规则已改变，请重新选择章节；旧书签已保留")
		}
		found := false
		for i, c := range book.Chapters {
			if c.ID == mark.ChapterID {
				start = i
				part = mark.Part
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("旧书签的章节不在当前版本，请重新选择章节")
		}
	}
	chunks := []audiobookChunk{}
	for _, c := range book.Chapters[start:] {
		for p, text := range splitBookText(c.Text, b.cfg.ChunkChars) {
			chunks = append(chunks, audiobookChunk{c.ID, c.Title, p, text})
		}
	}
	cursor := part
	if cursor < 0 || cursor >= len(chunks) || len(chunks) == 0 || chunks[cursor].ChapterID != book.Chapters[start].ID {
		return "", fmt.Errorf("所选章节没有可朗读的正文")
	}
	if mark, ok := b.bookmarks[book.ID]; chapterID == "" && ok && !mark.Completed && mark.ChunkHash != bookChunkHash(chunks[cursor].Text) {
		return "", fmt.Errorf("书签对应的正文已改变，请重新选择章节")
	}
	cfg := b.ttsConfig()
	cfg.Format = "wav"
	t := NewDoubaoTTS(cfg)
	t.dir = filepath.Join(b.dir, "audio")
	t.requireComplete = true
	// 用户选择听书时暂停旧媒体；准备合成期间不能让旧歌单自行切歌。
	if b.mediaPlaying() {
		if err := b.pause(); err != nil {
			return "", fmt.Errorf("暂停当前媒体失败：%w", err)
		}
	}
	if b.onSelect != nil {
		b.onSelect()
	}
	if b.session != nil {
		b.session.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &audiobookSession{book: book, chunks: chunks, cursor: cursor, audio: map[int]audiobookAudio{}, tts: t, ctx: ctx, cancel: cancel, status: "preparing"}
	b.session = s
	if err := b.saveBookmarkLocked(false); err != nil {
		s.cancel()
		s.status = "error"
		s.err = err.Error()
		return "", err
	}
	log.Printf("📖 [听书] 已选择《%s》/%s，第 %d 段起（按需缓存）", book.Title, chunks[cursor].ChapterTitle, chunks[cursor].Part+1)
	return fmt.Sprintf("已安排《%s》，从%s第%d段开始，确认语播完后起播。正文由服务直接读取并合成，模型不要输出正文或调用 speak 朗读。", book.Title, chunks[cursor].ChapterTitle, chunks[cursor].Part+1), nil
}

func (b *audiobookService) saveBookmarkLocked(completed bool) error {
	s := b.session
	if s == nil || len(s.chunks) == 0 {
		return nil
	}
	c := s.chunks[min(s.cursor, len(s.chunks)-1)]
	b.bookmarks[s.book.ID] = audiobookBookmark{BookID: s.book.ID, Version: s.book.Version, ChunkHash: bookChunkHash(c.Text), ChapterID: c.ChapterID, Part: c.Part, ChunkChars: b.cfg.ChunkChars, TextVersion: audiobookTextVersion, Completed: completed, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	raw, err := json.MarshalIndent(b.bookmarks, "", "  ")
	if err == nil {
		err = writeFileAtomic(filepath.Join(b.dir, "progress.json"), raw)
	}
	if err != nil {
		return fmt.Errorf("保存听书进度失败：%w", err)
	}
	return nil
}
func (b *audiobookService) status() audiobookStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	marks := map[string]audiobookBookmark{}
	for k, v := range b.bookmarks {
		marks[k] = v
	}
	out := audiobookStatus{Enabled: true, Status: "idle", Bookmarks: marks}
	if s := b.session; s != nil {
		out.Status, out.BookID, out.Title, out.Error = s.status, s.book.ID, s.book.Title, s.err
		out.Segment, out.TotalSegments = s.cursor+1, len(s.chunks)
		if len(s.chunks) > 0 {
			c := s.chunks[min(s.cursor, len(s.chunks)-1)]
			out.ChapterID, out.ChapterTitle, out.Part = c.ChapterID, c.ChapterTitle, c.Part+1
		}
		for i := range s.audio {
			if i >= s.cursor {
				out.Buffered++
			}
		}
	}
	return out
}
func (b *audiobookService) active() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session != nil && b.session.status != "stopped" && b.session.status != "completed" && b.session.status != "error"
}

func (b *audiobookService) control(action string) (string, error) {
	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.session
	if s == nil {
		return "", fmt.Errorf("还没有选择听书，请先选书和章节")
	}
	switch action {
	case "pause":
		if s.status == "stopped" || s.status == "completed" || s.status == "error" {
			return "", fmt.Errorf("当前没有可暂停的听书")
		}
		if s.status == "playing" && b.mediaURL() == s.url {
			if err := b.pause(); err != nil {
				return "", err
			}
		}
		s.status = "paused"
	case "resume":
		if s.status != "paused" && s.status != "interrupted" {
			return "", fmt.Errorf("当前没有可恢复的听书；服务重启或停止后请用 play_book 续听")
		}
		s.status = "interrupted"
	case "stop":
		if s.url != "" && b.mediaURL() == s.url {
			b.stop()
		}
		s.cancel()
		s.status = "stopped"
		if err := b.writePlaybackLocked(""); err != nil {
			return "", err
		}
	case "next_chapter", "previous_chapter":
		chapter := s.chunks[min(s.cursor, len(s.chunks)-1)].ChapterID
		idx := -1
		for i, c := range s.book.Chapters {
			if c.ID == chapter {
				idx = i
				break
			}
		}
		if action == "next_chapter" {
			idx++
		} else {
			idx--
		}
		if idx < 0 || idx >= len(s.book.Chapters) {
			return "", fmt.Errorf("已经没有相邻章节")
		}
		target := s.book.Chapters[idx]
		var chunks []audiobookChunk
		for _, c := range s.book.Chapters[idx:] {
			for p, text := range splitBookText(c.Text, b.cfg.ChunkChars) {
				chunks = append(chunks, audiobookChunk{c.ID, c.Title, p, text})
			}
		}
		if s.url != "" && b.mediaURL() == s.url {
			if err := b.pause(); err != nil {
				return "", err
			}
		}
		s.cancel()
		ctx, cancel := context.WithCancel(context.Background())
		b.session = &audiobookSession{book: s.book, chunks: chunks, audio: map[int]audiobookAudio{}, tts: s.tts, ctx: ctx, cancel: cancel, status: "preparing"}
		log.Printf("📖 [听书] 切换到%s", target.Title)
	default:
		return "", fmt.Errorf("不支持的听书操作")
	}
	if err := b.saveBookmarkLocked(b.session.cursor >= len(b.session.chunks)); err != nil {
		return "", err
	}
	return "听书操作已执行；正文不需要模型复述。", nil
}

// 在音乐操作已经持有 mediaMu 时调用；只取消听书后台工作，不操作新媒体。
func (b *audiobookService) relinquishLocked() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.session; s != nil {
		s.cancel()
		if s.status != "completed" {
			s.status = "stopped"
		}
	}
	if err := b.writePlaybackLocked(""); err != nil {
		log.Printf("⚠️ [听书] %v", err)
	}
}

func (b *audiobookService) beginDialog(turn uint64) {
	if b == nil {
		return
	}
	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dialogTurn = turn
	if s := b.session; s != nil && s.status == "playing" && b.mediaURL() == s.url {
		if err := b.pause(); err != nil {
			s.status = "error"
			s.err = "对话前暂停听书失败：" + err.Error()
			log.Printf("⚠️ [听书] %s", s.err)
			return
		}
		s.status = "interrupted"
	}
}
func (b *audiobookService) endDialog(turn uint64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dialogTurn == turn {
		b.dialogTurn = 0
	}
}

func (b *audiobookService) step() {
	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ready {
		if b.baseURL() == "" {
			return
		}
		b.ready = true
		if strings.Contains(b.mediaURL(), "/audiobooks/audio/") {
			b.stop()
			if err := b.writePlaybackLocked(""); err != nil {
				log.Printf("⚠️ [听书] %v", err)
			}
		}
	}
	s := b.session
	if s == nil || s.status == "stopped" || s.status == "completed" || s.status == "error" {
		return
	}
	if s.url != "" && b.mediaURL() != s.url {
		s.cancel()
		s.status = "stopped"
		return
	}
	// 单飞合成 + 当前段及后面有限 N 段，播放暂停时也不扩大窗口。
	if s.status != "paused" && time.Now().After(s.retryAt) && !b.synthesizing && time.Now().After(b.nextSynthesis) {
		for i := s.cursor; i < min(len(s.chunks), s.cursor+b.cfg.Prefetch+1); i++ {
			if _, ok := s.audio[i]; !ok {
				b.synthesizing = true
				go b.generate(s, i)
				break
			}
		}
	}
	if b.dialogTurn != 0 || b.busy() {
		return
	}
	if s.status == "interrupted" && s.url != "" && b.mediaURL() == s.url {
		if err := b.resume(); err != nil {
			s.status = "error"
			s.err = "恢复听书失败：" + err.Error()
		} else {
			s.status = "playing"
		}
		return
	}
	if s.status != "preparing" && s.status != "interrupted" {
		return
	}
	audio, ok := s.audio[s.cursor]
	if !ok {
		return
	}
	base := strings.TrimRight(b.baseURL(), "/")
	if base == "" {
		return
	}
	url := base + "/audiobooks/audio/" + audio.Name
	// 起播前记录所有权，服务崩溃或重启后不能落回旧音乐队列。
	if err := b.writePlaybackLocked(url); err != nil {
		s.status = "error"
		s.err = err.Error()
		return
	}
	if err := b.play(url, audio.DurationMS); err != nil {
		s.status = "error"
		s.err = "听书起播失败：" + err.Error()
		_ = b.writePlaybackLocked("")
		return
	}
	s.url, s.status = url, "playing"
	log.Printf("📖 [听书] 播放《%s》/%s 第%d段", s.book.Title, s.chunks[s.cursor].ChapterTitle, s.chunks[s.cursor].Part+1)
}
func (b *audiobookService) generate(s *audiobookSession, index int) {
	text := s.chunks[index].Text
	cached := s.tts.Cached(text)
	audio, err := b.synthesize(s.ctx, s.tts, text)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.synthesizing = false
	if !cached {
		b.nextSynthesis = time.Now().Add(time.Duration(b.cfg.SynthesisGapMS) * time.Millisecond)
	}
	if b.session != s || s.ctx.Err() != nil {
		return
	}
	if err != nil {
		s.failures++
		s.retryAt = time.Now().Add(time.Duration(s.failures) * 3 * time.Second)
		if s.failures >= 3 {
			s.status = "error"
			s.err = "连续三次合成失败，进度已保留：" + err.Error()
			s.cancel()
		}
		log.Printf("⚠️ [听书] 合成失败（%d/3）：%v", s.failures, err)
		return
	}
	s.failures = 0
	s.retryAt = time.Time{}
	s.audio[index] = audio
	log.Printf("📦 [听书] 第%d段就绪（缓存=%v，时长=%dms）", index+1, cached, audio.DurationMS)
}

// 返回 handled 防止一本书播完后落回旧网易云歌单；只由设备真实结束通知推进。
// 调用方持有 mediaMu。
func (b *audiobookService) endedLocked() (handled, advanced bool) {
	if b == nil {
		return false, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	url := b.mediaURL()
	s := b.session
	if !strings.Contains(url, "/audiobooks/audio/") {
		return false, false
	}
	if s == nil || s.url != url || s.status != "playing" {
		return true, false
	}
	s.cursor++
	if s.cursor >= len(s.chunks) {
		s.status = "completed"
		s.cancel()
		if err := b.writePlaybackLocked(""); err != nil {
			s.err = err.Error()
		}
		if err := b.saveBookmarkLocked(true); err != nil {
			s.err = err.Error()
		}
		log.Printf("📖 [听书] 《%s》已播完", s.book.Title)
		return true, false
	}
	s.url = ""
	s.status = "preparing"
	// 已播完的内存队列释放；磁盘音频永久保留，不因播放完成删除。
	for i := range s.audio {
		if i < s.cursor {
			delete(s.audio, i)
		}
	}
	if err := b.saveBookmarkLocked(false); err != nil {
		s.status = "error"
		s.err = err.Error()
		return true, false
	}
	return true, true
}

// WAV 时长从真实字节数计算，拒绝截断文件；兼容流式 WAV 的未知 data 长度。
func audiobookWAVDuration(filename string) (int, error) {
	f, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	head := make([]byte, 12)
	if _, err = io.ReadFull(f, head); err != nil || string(head[:4]) != "RIFF" || string(head[8:]) != "WAVE" {
		return 0, fmt.Errorf("听书合成结果不是有效 WAV")
	}
	var byteRate uint32
	for pos := int64(12); pos+8 <= info.Size(); {
		h := make([]byte, 8)
		if _, err = f.ReadAt(h, pos); err != nil {
			return 0, err
		}
		n := int64(binary.LittleEndian.Uint32(h[4:]))
		start := pos + 8
		switch string(h[:4]) {
		case "fmt ":
			if n < 16 || n > 4096 {
				return 0, fmt.Errorf("WAV 格式块无效")
			}
			v := make([]byte, 16)
			if _, err = f.ReadAt(v, start); err != nil {
				return 0, err
			}
			byteRate = binary.LittleEndian.Uint32(v[8:12])
		case "data":
			if n == 0xffffffff || n == 0 {
				n = info.Size() - start
			}
			if byteRate == 0 || n <= 0 || n > info.Size()-start {
				return 0, fmt.Errorf("WAV 音频为空或不完整")
			}
			ms := int(n * 1000 / int64(byteRate))
			if ms <= 0 {
				return 0, fmt.Errorf("WAV 时长无效")
			}
			return ms, nil
		}
		pos = start + n + n%2
	}
	return 0, fmt.Errorf("WAV 缺少音频数据")
}

func (b *audiobookService) writePlaybackLocked(url string) error {
	raw, err := json.Marshal(struct {
		URL string `json:"url"`
	}{url})
	if err == nil {
		err = writeFileAtomic(filepath.Join(b.dir, "playback.json"), raw)
	}
	if err != nil {
		return fmt.Errorf("保存听书播放记录失败：%w", err)
	}
	return nil
}

func (b *audiobookService) reading() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session != nil && b.session.status == "playing"
}
