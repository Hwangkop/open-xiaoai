package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type bilibiliTestTransport func(*http.Request) (*http.Response, error)

func (f bilibiliTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func bilibiliTestJSON(v any) (*http.Response, error) {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}, nil
}
func testBVID(i int) string { return fmt.Sprintf("BV%010d", i) }

func catalogTestService(t *testing.T, view func(string) any, page func(int) any) *bilibiliService {
	t.Helper()
	s, err := newBilibiliService(filepath.Join(t.TempDir(), "config.yaml"), BilibiliConfig{})
	if err != nil {
		t.Fatal(err)
	}
	s.bootstrapped = true
	s.wbiImgKey, s.wbiSubKey = "7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45"
	s.wbiFetchedAt = time.Now()
	s.client.Transport = bilibiliTestTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/view"):
			return bilibiliTestJSON(map[string]any{"code": 0, "data": view(r.URL.Query().Get("bvid"))})
		case strings.HasSuffix(r.URL.Path, "seasons_archives_list") && page != nil:
			n, _ := strconv.Atoi(r.URL.Query().Get("page_num"))
			return bilibiliTestJSON(page(n))
		case strings.HasSuffix(r.URL.Path, "/playurl"):
			return bilibiliTestJSON(map[string]any{"code": 0, "data": map[string]any{"dash": map[string]any{"audio": []any{map[string]any{"id": 30280, "baseUrl": "https://example.invalid/audio", "bandwidth": 320000, "codecs": "mp4a.40.2"}}}}})
		default:
			return nil, fmt.Errorf("意外请求：%s", r.URL.Path)
		}
	})
	return s
}

func catalogTestView(bvid string) map[string]any {
	return map[string]any{"bvid": bvid, "aid": 1, "cid": 11, "title": "测试视频", "duration": 240, "owner": map[string]any{"mid": 42, "name": "测试作者"}, "pages": []any{map[string]any{"cid": 11, "part": "第一部分", "duration": 240}, map[string]any{"cid": 12, "part": "第二部分", "duration": 180}}}
}

func TestBilibiliCatalogReference(t *testing.T) {
	bvid := testBVID(1)
	for _, ref := range []string{bvid, "https://www.bilibili.com/video/" + bvid + "/?p=2&vd_source=ignored"} {
		id, page, err := parseBilibiliReference(ref)
		if err != nil || id != bvid || page < 1 {
			t.Fatalf("解析失败：%q %q %d %v", ref, id, page, err)
		}
		if strings.Contains(ref, "p=2") && page != 2 {
			t.Fatal("丢失分 P 选择")
		}
	}
	for _, ref := range []string{"https://evil.invalid/video/" + bvid, "https://www.bilibili.com.evil.invalid/video/" + bvid, "https://user:secret@www.bilibili.com/video/" + bvid, "http://127.0.0.1/video/" + bvid, "https://www.bilibili.com/video/" + bvid + "?p=0"} {
		if _, _, err := parseBilibiliReference(ref); err == nil {
			t.Fatalf("接受了无效链接：%s", ref)
		}
	}
}

func TestBilibiliCatalogPartsAndCollectionPagination(t *testing.T) {
	var calls atomic.Int32
	s := catalogTestService(t, func(bvid string) any {
		v := catalogTestView(bvid)
		v["ugc_season"] = map[string]any{"id": 99, "title": "私人尊享", "ep_count": 106, "sections": []any{}}
		return v
	}, func(page int) any {
		calls.Add(1)
		items := []any{}
		for i := (page-1)*30 + 1; i <= min(page*30, 106); i++ {
			items = append(items, map[string]any{"bvid": testBVID(i), "title": fmt.Sprintf("视频%d", i), "duration": i})
		}
		return map[string]any{"code": 0, "data": map[string]any{"page": map[string]any{"total": 106}, "archives": items}}
	})
	c, err := s.catalog(context.Background(), "https://www.bilibili.com/video/"+testBVID(1)+"?p=2")
	if err != nil {
		t.Fatal(err)
	}
	if c.SelectedCID != 12 || len(c.Parts) != 2 || c.Collection.Total != 106 || len(c.Collection.Items) != 106 || calls.Load() != 4 {
		t.Fatalf("目录或分页不完整：%+v 次数=%d", c, calls.Load())
	}
	if c.Collection.Items[105].BVID != testBVID(106) {
		t.Fatal("最后一页被遗漏")
	}
}

func TestBilibiliCatalogRejectsIncompletePages(t *testing.T) {
	for _, mode := range []string{"重复页", "中途失败"} {
		t.Run(mode, func(t *testing.T) {
			s := catalogTestService(t, func(bvid string) any {
				v := catalogTestView(bvid)
				v["ugc_season"] = map[string]any{"id": 99, "title": "合集", "ep_count": 60}
				return v
			}, func(page int) any {
				if mode == "中途失败" && page == 2 {
					return map[string]any{"code": -412}
				}
				items := []any{}
				for i := 1; i <= 30; i++ {
					items = append(items, map[string]any{"bvid": testBVID(i)})
				}
				return map[string]any{"code": 0, "data": map[string]any{"page": map[string]any{"total": 60}, "archives": items}}
			})
			if c, err := s.catalog(context.Background(), testBVID(1)); err == nil || c != nil {
				t.Fatal("把不完整目录当成完整合集")
			}
		})
	}
}

func TestBilibiliCatalogPreservesFullViewOrder(t *testing.T) {
	s := catalogTestService(t, func(bvid string) any {
		v := catalogTestView(bvid)
		v["ugc_season"] = map[string]any{"id": 99, "title": "合集", "ep_count": 2, "sections": []any{map[string]any{"title": "正片", "episodes": []any{map[string]any{"bvid": testBVID(2), "cid": 12, "title": "先播"}, map[string]any{"bvid": testBVID(1), "cid": 11, "title": "后播"}}}}}
		return v
	}, nil)
	c, err := s.catalog(context.Background(), testBVID(1))
	if err != nil {
		t.Fatal(err)
	}
	if c.Collection.Items[0].BVID != testBVID(2) || c.Collection.Items[0].Section != "正片" {
		t.Fatal("改变了作者的目录顺序")
	}
}

func cachedBilibiliTestAdmin(t *testing.T) (*adminServer, chan string) {
	t.Helper()
	a := musicSyncAdmin(t, func(script string) string {
		if strings.Contains(script, "player_get_play_status") {
			return `{"info":{"status":3}}`
		}
		return `{"code":0}`
	})
	a.bilibili = catalogTestService(t, func(id string) any { return catalogTestView(id) }, nil)
	a.mediaBaseURL = func() string { return "http://example.invalid:4399" }
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(filepath.Join(a.bilibili.dir, testBVID(i)+"-11.mp3"), []byte("模拟已缓存音轨"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plays := make(chan string, 10)
	original := a.playURL
	a.playURL = func(u string) error {
		if err := original(u); err != nil {
			return err
		}
		plays <- u
		return nil
	}
	t.Cleanup(func() { a.music.opMu.Lock(); a.cancelBilibili(false, "stopped"); a.music.opMu.Unlock() })
	return a, plays
}

func waitBilibiliPlay(t *testing.T, plays <-chan string, id string) {
	t.Helper()
	select {
	case u := <-plays:
		if !strings.Contains(u, id) {
			t.Fatalf("播放了错误来源：%s", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("没有起播")
	}
}

func TestBilibiliQueueAdvanceNeverReturnsToNetease(t *testing.T) {
	a, plays := cachedBilibiliTestAdmin(t)
	a.music.queue, a.music.idx, a.music.queueName = testSongs(5), 0, "旧网易云歌单"
	items := []bilibiliCatalogItem{{BVID: testBVID(1), CID: 11, Title: "视频1"}, {BVID: testBVID(2), CID: 11, Title: "视频2"}}
	if _, err := a.requestBilibiliQueue("合集", items, 0, false); err != nil {
		t.Fatal(err)
	}
	waitBilibiliPlay(t, plays, testBVID(1))
	finishTestMusic(a)
	waitBilibiliPlay(t, plays, testBVID(2))
	finishTestMusic(a)
	select {
	case u := <-plays:
		t.Fatalf("队列结束后发生抢播：%s", u)
	default:
	}
	a.music.mu.Lock()
	defer a.music.mu.Unlock()
	if a.music.bilibili.Index != 1 || a.music.bilibili.Status != "ended" || a.music.idx != 0 {
		t.Fatal("B 站播完修改或继续了网易云队列")
	}
}

func TestBilibiliStopCancelsSlowPreparation(t *testing.T) {
	a, plays := cachedBilibiliTestAdmin(t)
	entered := make(chan struct{})
	original := a.bilibili.client.Transport
	a.bilibili.client.Transport = bilibiliTestTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/playurl") {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return original.RoundTrip(r)
	})
	if _, err := a.requestBilibiliQueue("合集", []bilibiliCatalogItem{{BVID: testBVID(1), CID: 11}}, 0, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("没有开始准备")
	}
	start := time.Now()
	a.stopPlayback()
	if time.Since(start) > time.Second {
		t.Fatal("停止操作被下载阻塞")
	}
	a.bilibili.mu.Lock()
	a.bilibili.mu.Unlock() // 等已取消的准备释放资源。
	select {
	case u := <-plays:
		t.Fatalf("停止后旧下载仍起播：%s", u)
	default:
	}
	a.music.mu.Lock()
	defer a.music.mu.Unlock()
	if a.music.bilibili.Status != "stopped" {
		t.Fatal("取消错误覆盖了停止状态")
	}
}

func TestBilibiliPauseBeforeReadyAndRestoreQueue(t *testing.T) {
	a, plays := cachedBilibiliTestAdmin(t)
	a.speaker.markSpeaking(true)
	if _, err := a.requestBilibiliQueue("合集", []bilibiliCatalogItem{{BVID: testBVID(1), CID: 11}}, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := a.controlQueuedMusic(context.Background(), "pause", nil); err != nil {
		t.Fatal(err)
	}
	a.speaker.markSpeaking(false)
	select {
	case <-plays:
		t.Fatal("暂停期间开始播放")
	default:
	}
	restored := &adminServer{music: newMusicState()}
	restored.loadMusic(a.music.path)
	if restored.music.bilibili == nil || restored.music.bilibili.Status != "paused" {
		t.Fatal("没有恢复 B 站队列")
	}
	if err := a.controlQueuedMusic(context.Background(), "resume", nil); err != nil {
		t.Fatal(err)
	}
	waitBilibiliPlay(t, plays, testBVID(1))
	w := httptest.NewRecorder()
	a.handleMusicPlayer(w, httptest.NewRequest("GET", "/admin/api/music", nil))
	var state struct {
		Source string `json:"source"`
		Index  int    `json:"index"`
	}
	json.Unmarshal(w.Body.Bytes(), &state)
	if state.Source != "bilibili" || state.Index != 0 {
		t.Fatal("管理页仍显示网易云队列")
	}
}

func TestBilibiliCatalogLive(t *testing.T) {
	if os.Getenv("BILIBILI_CATALOG_LIVE") != "1" {
		t.Skip("设置 BILIBILI_CATALOG_LIVE=1 后只读验证真实合集目录")
	}
	s, err := newBilibiliService(filepath.Join(t.TempDir(), "config.yaml"), BilibiliConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := s.catalog(ctx, "BV1qFuQz2EVx")
	if err != nil {
		t.Fatal(err)
	}
	if c.Collection == nil || c.Collection.Total != len(c.Collection.Items) || len(c.Parts) != 1 {
		t.Fatal("真实合集目录不完整")
	}
	t.Logf("合集=%s，视频=%d，当前视频分 P=%d，时长=%d 秒", c.Collection.Title, len(c.Collection.Items), len(c.Parts), c.Video.Duration)
}

func TestBilibiliReplacedPreparationCannotStealPlayback(t *testing.T) {
	a, plays := cachedBilibiliTestAdmin(t)
	a.speaker.markSpeaking(true)
	if _, err := a.requestBilibiliQueue("旧合集", []bilibiliCatalogItem{{BVID: testBVID(1), CID: 11}}, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.requestBilibiliQueue("新合集", []bilibiliCatalogItem{{BVID: testBVID(2), CID: 11}}, 0, false); err != nil {
		t.Fatal(err)
	}
	a.speaker.markSpeaking(false)
	waitBilibiliPlay(t, plays, testBVID(2))
	a.bilibili.mu.Lock()
	a.bilibili.mu.Unlock()
	select {
	case u := <-plays:
		t.Fatalf("被替换的队列抢播：%s", u)
	default:
	}
}

func TestBilibiliPendingPreparationYieldsToBookSelection(t *testing.T) {
	a, plays := cachedBilibiliTestAdmin(t)
	a.speaker.markSpeaking(true)
	if _, err := a.requestBilibiliQueue("旧合集", []bilibiliCatalogItem{{BVID: testBVID(1), CID: 11}}, 0, false); err != nil {
		t.Fatal(err)
	}
	b, _, _ := testBooks(t)
	b.mediaMu = &a.music.opMu
	b.onSelect = func() { a.cancelBilibili(true, ""); a.persistMusic() }
	if _, err := b.request("friend", bookChapter(t, b, "第一章：醒悟")); err != nil {
		t.Fatal(err)
	}
	a.speaker.markSpeaking(false)
	a.bilibili.mu.Lock()
	a.bilibili.mu.Unlock()
	a.music.mu.Lock()
	pending := a.music.bilibili
	a.music.mu.Unlock()
	if pending != nil {
		t.Fatal("听书已经选定，旧 B 站队列仍有起播资格")
	}
	select {
	case u := <-plays:
		t.Fatalf("选书后旧下载抢播：%s", u)
	default:
	}
}

func TestBilibiliVoicePauseUsesQueueControl(t *testing.T) {
	called := ""
	e := &Engine{musicControl: func(_ context.Context, action string) error { called = action; return nil }, musicStop: func() { t.Fatal("暂停不能改为停止") }}
	e.execMusicCommand("stop", "暂停")
	if called != "pause" {
		t.Fatal("语音暂停没有经过当前队列控制")
	}
}

func TestBilibiliReadyAudioWaitsForWholeDialogue(t *testing.T) {
	a, plays := cachedBilibiliTestAdmin(t)
	a.engine = &Engine{}
	a.engine.activeDialog.Store(1)
	entered := make(chan struct{})
	original := a.bilibili.client.Transport
	a.bilibili.client.Transport = bilibiliTestTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/playurl") {
			close(entered)
		}
		return original.RoundTrip(r)
	})
	if _, err := a.requestBilibiliQueue("合集", []bilibiliCatalogItem{{BVID: testBVID(1), CID: 11}}, 0, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("没有准备音频")
	}
	a.bilibili.mu.Lock()
	a.bilibili.mu.Unlock()
	select {
	case <-plays:
		t.Fatal("思考或 TTS 句间空隙提前起播")
	default:
	}
	a.engine.activeDialog.Store(0)
	waitBilibiliPlay(t, plays, testBVID(1))
}
