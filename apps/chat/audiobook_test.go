package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func testBookKnowledge(t *testing.T) *knowledgeService {
	t.Helper()
	root := t.TempDir()
	content := filepath.Join(root, "text")
	if err := os.MkdirAll(content, 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Chapter10.md": "# 第十章：后来\n" + strings.Repeat("这是后面的内容。", 30),
		"Chapter1.md":  "# 第一章：醒悟\n## 一、开头\n" + strings.Repeat("这是直接来自原文的句子，模型不应看到这段独特正文。", 25),
		"Chapter0.md":  "# 第〇章：困境\n这是序章。",
		"README.md":    "# 仓库使用说明\n不要把这份说明当正文。",
	}
	manifest := knowledgeManifest{Title: "把时间当作朋友", Files: map[string]knowledgeFileExpectation{}}
	for name, text := range files {
		data := []byte(text)
		h := sha256.Sum256(data)
		manifest.Files[name] = knowledgeFileExpectation{Bytes: int64(len(data)), SHA256: hex.EncodeToString(h[:])}
		if err := os.WriteFile(filepath.Join(content, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(root, "manifest.json"), data, 0644)
	reg := knowledgeRegistry{SchemaVersion: 1, Generation: 1, Sources: []knowledgeSource{{ID: "friend", Name: "把时间当作朋友", Kind: "book", Author: "李笑来", Enabled: true, PublishedVersion: "v1", Versions: []knowledgeVersion{{ID: "v1", ContentRoot: "text", Manifest: "manifest.json"}}}}}
	data, _ = json.Marshal(reg)
	os.WriteFile(filepath.Join(root, "registry.json"), data, 0644)
	return &knowledgeService{root: root}
}
func testBookWAV() []byte {
	b := new(bytes.Buffer)
	b.WriteString("RIFF")
	binary.Write(b, binary.LittleEndian, uint32(36+48000))
	b.WriteString("WAVEfmt ")
	binary.Write(b, binary.LittleEndian, uint32(16))
	binary.Write(b, binary.LittleEndian, uint16(1))
	binary.Write(b, binary.LittleEndian, uint16(1))
	binary.Write(b, binary.LittleEndian, uint32(24000))
	binary.Write(b, binary.LittleEndian, uint32(48000))
	binary.Write(b, binary.LittleEndian, uint16(2))
	binary.Write(b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(b, binary.LittleEndian, uint32(48000))
	b.Write(make([]byte, 48000))
	return b.Bytes()
}

type fakeBookDevice struct {
	mu                            sync.Mutex
	url                           string
	plays, pauses, resumes, stops int
	playing                       bool
}

func (d *fakeBookDevice) current() string { d.mu.Lock(); defer d.mu.Unlock(); return d.url }
func testBooks(t *testing.T) (*audiobookService, *fakeBookDevice, *atomic.Int32) {
	t.Helper()
	b, err := newAudiobookService(filepath.Join(t.TempDir(), "config.yaml"), AudiobookConfig{ChunkChars: 80, Prefetch: 2}, testBookKnowledge(t), &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeBookDevice{}
	calls := &atomic.Int32{}
	b.ttsConfig = func() TTSConfig { return TTSConfig{Speaker: "test-voice"} }
	b.baseURL = func() string { return "http://speaker-server" }
	b.busy = func() bool { return false }
	b.mediaURL = d.current
	b.mediaPlaying = func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.playing }
	b.play = func(url string, _ int) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.url = url
		d.plays++
		d.playing = true
		return nil
	}
	b.pause = func() error { d.mu.Lock(); defer d.mu.Unlock(); d.pauses++; d.playing = false; return nil }
	b.resume = func() error { d.mu.Lock(); defer d.mu.Unlock(); d.resumes++; d.playing = true; return nil }
	b.stop = func() { d.mu.Lock(); defer d.mu.Unlock(); d.stops++; d.playing = false }
	b.synthesize = func(ctx context.Context, tts *DoubaoTTS, text string) (audiobookAudio, error) {
		if err := ctx.Err(); err != nil {
			return audiobookAudio{}, err
		}
		name := tts.cacheName(text)
		if !tts.Cached(text) {
			calls.Add(1)
			if err := os.WriteFile(filepath.Join(tts.dir, name), testBookWAV(), 0644); err != nil {
				return audiobookAudio{}, err
			}
		}
		return audiobookAudio{name, 1000}, nil
	}
	t.Cleanup(b.close)
	return b, d, calls
}
func bookTick(t *testing.T, b *audiobookService) {
	t.Helper()
	b.mu.Lock()
	b.nextSynthesis = time.Time{}
	b.mu.Unlock()
	b.step()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		busy := b.synthesizing
		b.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("合成没有完成")
		}
		time.Sleep(time.Millisecond)
	}
}
func bookChapter(t *testing.T, b *audiobookService, title string) string {
	t.Helper()
	books, err := b.catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range books[0].Chapters {
		if c.Title == title {
			return c.ID
		}
	}
	t.Fatalf("章节未找到：%s", title)
	return ""
}
func TestAudiobookCatalogAndDirectText(t *testing.T) {
	b, _, _ := testBooks(t)
	books, err := b.catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || len(books[0].Chapters) != 3 {
		t.Fatalf("目录不对：%+v", books)
	}
	if books[0].Chapters[0].Title != "第〇章：困境" || books[0].Chapters[1].Title != "第一章：醒悟" || books[0].Chapters[2].Title != "第十章：后来" {
		t.Fatalf("数字排序错误：%+v", books[0].Chapters)
	}
	a := &adminServer{audiobooks: b}
	text, err := a.runAudiobookTool(mcpToolCall{Name: "list_book_chapters", Arguments: map[string]any{"book": "把时间当做朋友"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "独特正文") {
		t.Fatal("目录把整章原文送给模型了")
	}
	id := books[0].Chapters[1].ID
	if _, err := b.request("friend", id); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session.chunks[0].ChapterID != id || !strings.Contains(b.session.chunks[0].Text, "独特正文") {
		t.Fatal("没有从所选第一章直接读取正文")
	}
}
func TestAudiobookBoundedPrefetchAndPersistentCache(t *testing.T) {
	b, d, calls := testBooks(t)
	id := bookChapter(t, b, "第一章：醒悟")
	if _, err := b.request("friend", id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		bookTick(t, b)
	}
	if calls.Load() > 3 || calls.Load() < 1 || b.status().Buffered != 3 {
		t.Fatalf("预生成超出窗口：请求=%d，状态=%+v", calls.Load(), b.status())
	}
	d.mu.Lock()
	plays := d.plays
	d.mu.Unlock()
	if plays != 1 {
		t.Fatalf("准备过程重复起播：%d", plays)
	}
	initialCalls := calls.Load()
	if _, err := b.control("stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.request("friend", id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		bookTick(t, b)
	}
	if calls.Load() != initialCalls {
		t.Fatalf("重复收听重新合成了：%d", calls.Load())
	}
	if b.session.tts.WithVoice("different", "").cacheName(b.session.chunks[0].Text) == b.session.tts.cacheName(b.session.chunks[0].Text) {
		t.Fatal("不同声音复用了相同音频键")
	}
}
func TestAudiobookDialoguePauseResumeAndExplicitPause(t *testing.T) {
	b, d, _ := testBooks(t)
	id := bookChapter(t, b, "第一章：醒悟")
	b.beginDialog(1)
	if _, err := b.request("friend", id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		bookTick(t, b)
	}
	d.mu.Lock()
	plays := d.plays
	d.mu.Unlock()
	if plays != 0 {
		t.Fatal("确认语之前抢先播放了正文")
	}
	b.endDialog(1)
	bookTick(t, b)
	b.beginDialog(2)
	if b.status().Status != "interrupted" {
		t.Fatal("插话没有暂停书")
	}
	b.beginDialog(3)
	b.endDialog(2)
	bookTick(t, b)
	if b.status().Status != "interrupted" {
		t.Fatal("旧对话的收尾恢复了新对话中的书")
	}
	b.endDialog(3)
	bookTick(t, b)
	if b.status().Status != "playing" {
		t.Fatal("回答后未恢复")
	}
	b.beginDialog(4)
	if _, err := b.control("pause"); err != nil {
		t.Fatal(err)
	}
	b.endDialog(4)
	for i := 0; i < 3; i++ {
		bookTick(t, b)
	}
	if b.status().Status != "paused" {
		t.Fatal("用户明确暂停被自动恢复了")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.plays != 1 || d.resumes != 1 {
		t.Fatalf("重复恢复或重头起播：播放%d 恢复%d", d.plays, d.resumes)
	}
}
func TestAudiobookCompletionCheckpointAndRestart(t *testing.T) {
	b, _, _ := testBooks(t)
	id := bookChapter(t, b, "第一章：醒悟")
	if _, err := b.request("friend", id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		bookTick(t, b)
	}
	b.mediaMu.Lock()
	handled, next := b.endedLocked()
	b.mediaMu.Unlock()
	if !handled || !next {
		t.Fatal("未推进到下一段")
	}
	mark := b.status().Bookmarks["friend"]
	if mark.Part != 1 {
		t.Fatalf("书签错误：%+v", mark)
	}
	restored, err := newAudiobookService(filepath.Join(filepath.Dir(b.dir), "config.yaml"), b.cfg, b.knowledge, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	if restored.status().Status != "idle" || restored.bookmarks["friend"].Part != 1 || restored.initialURL == "" {
		t.Fatalf("没有恢复书签或播放所有权：%+v", restored.status())
	}
	restored.ttsConfig = b.ttsConfig
	restored.pause = b.pause
	restored.mediaPlaying = b.mediaPlaying
	if _, err := restored.request("friend", ""); err != nil {
		t.Fatal(err)
	}
	if restored.session.cursor != 1 {
		t.Fatal("从书签续听时回到第一段了")
	}
	restored.close()
	b.mediaMu.Lock()
	b.relinquishLocked()
	b.mediaMu.Unlock()
	if b.status().Status != "stopped" {
		t.Fatal("切到音乐后听书仍在后台推进")
	}
	before := b.status().Segment
	b.mediaMu.Lock()
	_, _ = b.endedLocked()
	b.mediaMu.Unlock()
	if b.status().Segment != before {
		t.Fatal("取消后仍推进了书签")
	}
}
func TestAudiobookOldSynthesisCannotStartNewChapter(t *testing.T) {
	b, d, _ := testBooks(t)
	first := bookChapter(t, b, "第一章：醒悟")
	if _, err := b.request("friend", first); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	b.synthesize = func(ctx context.Context, tts *DoubaoTTS, text string) (audiobookAudio, error) {
		requests.Add(1)
		close(started)
		<-release
		return audiobookAudio{tts.cacheName(text), 1000}, ctx.Err()
	}
	b.step()
	<-started
	for i := 0; i < 10; i++ {
		b.step()
	}
	if requests.Load() != 1 {
		t.Fatal("合成没有串行")
	}
	if _, err := b.control("next_chapter"); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		active := b.synthesizing
		b.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("取消超时")
		}
		time.Sleep(time.Millisecond)
	}
	if b.status().Buffered != 0 {
		t.Fatal("旧合成结果写进了新章节")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.plays != 0 {
		t.Fatal("取消后的旧内容出声")
	}
}
func TestAudiobookMarkdownAndChunkBounds(t *testing.T) {
	raw := "# 第一章\n你好，**朋友**。\n![图片](a.png)\n[可读文字](https://example.com)\n```python\nprint('不读代码')\n```\n| 视觉表格 |\n后面的文字。"
	plain := cleanBookText(raw)
	for _, bad := range []string{"不读代码", "图片", "视觉表格", "https://", "**"} {
		if strings.Contains(plain, bad) {
			t.Fatalf("标记处理错误：%s", plain)
		}
	}
	if !strings.Contains(plain, "可读文字") || !strings.Contains(plain, "后面的文字") {
		t.Fatal("丢失正文")
	}
	text := strings.Repeat("一段完整的句子，不能丢掉。", 80)
	parts := splitBookText(text, 80)
	for _, p := range parts {
		if utf8.RuneCountInString(p) > 80 {
			t.Fatal("超过分段上限")
		}
	}
	if strings.Join(parts, "") != text {
		t.Fatal("切段丢字或重排")
	}
	one := bookFileChapters("README.md", "# 书名\n前置正文。\n## 第一部分\n正文一。\n## 第二部分\n正文二。", "v1", true)
	if len(one) != 3 || one[0].Title != "开篇" || one[2].Title != "第二部分" {
		t.Fatalf("单文件整书目录错误：%+v", one)
	}
}
func TestAudiobookWAVAndAudioServing(t *testing.T) {
	b, _, _ := testBooks(t)
	name := strings.Repeat("a", 32) + ".wav"
	filename := filepath.Join(b.dir, "audio", name)
	wav := testBookWAV()
	os.WriteFile(filename, wav, 0644)
	if duration, err := audiobookWAVDuration(filename); err != nil || duration != 1000 {
		t.Fatalf("时长：%d %v", duration, err)
	}
	os.WriteFile(filename, wav[:len(wav)-100], 0644)
	if _, err := audiobookWAVDuration(filename); err == nil {
		t.Fatal("把半段 WAV 当成了完整缓存")
	}
	os.WriteFile(filename, wav, 0644)
	a := &adminServer{audiobooks: b}
	w := httptest.NewRecorder()
	a.handleAudiobookAudio(w, httptest.NewRequest(http.MethodGet, "/audiobooks/audio/"+name, nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	a.handleAudiobookAudio(w, httptest.NewRequest(http.MethodGet, "/audiobooks/audio/../progress.json", nil))
	if w.Code != 404 {
		t.Fatal("暴露了进度文件")
	}
	r := httptest.NewRequest(http.MethodGet, "/audiobooks/audio/"+name, nil)
	r.Header.Set("Range", "bytes=0-43")
	w = httptest.NewRecorder()
	a.handleAudiobookAudio(w, r)
	if w.Code != 206 || w.Body.Len() != 44 {
		t.Fatalf("无法按范围拉取：%d %d", w.Code, w.Body.Len())
	}
}
func TestAudiobookCatalogRejectsTampering(t *testing.T) {
	k := testBookKnowledge(t)
	os.WriteFile(filepath.Join(k.root, "text", "Chapter1.md"), []byte("替换过的内容"), 0644)
	if _, err := k.audiobookBooks(); err == nil {
		t.Fatal("朗读了未通过知识库清单校验的正文")
	}
}
func TestAudiobookTTSLive(t *testing.T) {
	if os.Getenv("XIAOAI_AUDIOBOOK_TTS_LIVE") != "1" {
		t.Skip("显式设置 XIAOAI_AUDIOBOOK_TTS_LIVE=1 才连接豆包")
	}
	tts := NewDoubaoTTS(TTSConfig{Format: "wav"})
	tts.dir = t.TempDir()
	tts.requireComplete = true
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	text := "这是听书功能的语音测试。书的正文直接从知识库送入语音合成，生成后保存在本地，下一次可以直接收听。"
	name, err := tts.Synthesize(ctx, text)
	if err != nil {
		t.Fatal(err)
	}
	duration, err := audiobookWAVDuration(filepath.Join(tts.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if !tts.Cached(text) {
		t.Fatal("缓存未保存")
	}
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	again, err := tts.Synthesize(cancelled, text)
	if err != nil || again != name {
		t.Fatalf("已有缓存仍需要联网：%s %v", again, err)
	}
	fmt.Printf("📖 听书真实合成通过：%s，%dms；取消网络上下文后仍可读取同一缓存\n", name, duration)
}

func TestAudiobookRepositoryCatalog(t *testing.T) {
	root, err := filepath.Abs("../../knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "books", "time-as-a-friend", "original", "Chapter1.md")); err != nil {
		t.Skip("本地尚未导入公开书籍原文")
	}
	k := &knowledgeService{root: root}
	books, err := k.audiobookBooks()
	if err != nil {
		t.Fatal(err)
	}
	if len(books) < 4 {
		t.Fatalf("现有书架不完整：%d", len(books))
	}
	for _, book := range books {
		if len(book.Chapters) == 0 {
			t.Fatalf("《%s》没有章节", book.Title)
		}
		for _, chapter := range book.Chapters {
			if chapter.Text == "" || chapter.ID == "" {
				t.Fatalf("《%s》章节无效", book.Title)
			}
		}
		t.Logf("《%s》：%d 个章节，首章为 %s", book.Title, len(book.Chapters), book.Chapters[0].Title)
	}
}

func TestAudiobookPCASRDoesNotExecuteBookContents(t *testing.T) {
	b, _, _ := testBooks(t)
	id := bookChapter(t, b, "第一章：醒悟")
	if _, err := b.request("friend", id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		bookTick(t, b)
	}
	e := NewEngine(defaultConfig(), NewSpeaker())
	e.audiobooks = b
	// 正文里的这些词不能被当作裸指令，也不能消耗对话窗口。
	e.armDialog()
	before := e.armedUntil.Load()
	e.OnPCMUtterance("停止")
	e.OnPCMUtterance("下一首")
	e.OnPCMUtterance("帮我播放别的内容")
	if b.status().Status != "playing" || e.armedUntil.Load() != before {
		t.Fatal("书的自回声执行了指令或消耗了对话窗口")
	}
}
func TestAudiobookSingleChapterAndSetextTitles(t *testing.T) {
	chapters := bookFileChapters("ch01.md", "（一）千万别装\n==============\n正文。\n\n# 正文里的强调标题\n还在同一章。", "v1", false)
	if len(chapters) != 1 || chapters[0].Title != "（一）千万别装" || strings.Contains(chapters[0].Text, "====") {
		t.Fatalf("多文件的真实章界和标题处理错误：%+v", chapters)
	}
}
