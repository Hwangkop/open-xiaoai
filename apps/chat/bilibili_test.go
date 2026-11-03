package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBilibiliHelpers(t *testing.T) {
	if got := parseBilibiliDuration("4:19"); got != 259 {
		t.Fatalf("4:19 = %d, want 259", got)
	}
	if got := parseBilibiliDuration("1:02:03"); got != 3723 {
		t.Fatalf("1:02:03 = %d, want 3723", got)
	}
	if got := parseBilibiliDuration("bad"); got != 0 {
		t.Fatalf("bad duration = %d, want 0", got)
	}
	if got := cleanBilibiliText("【<em class=\"keyword\">周杰伦</em>】&amp; 合集"); got != "【周杰伦】& 合集" {
		t.Fatalf("clean title = %q", got)
	}
	if got := normalizeBilibiliImage("//i0.hdslb.com/cover.jpg"); got != "https://i0.hdslb.com/cover.jpg" {
		t.Fatalf("normalize cover = %q", got)
	}
}

func TestBilibiliLiveSearchAndResolve(t *testing.T) {
	if os.Getenv("BILIBILI_LIVE_TEST") != "1" {
		t.Skip("设置 BILIBILI_LIVE_TEST=1 后运行 B 站线上链路测试")
	}
	root := t.TempDir()
	service, err := newBilibiliService(filepath.Join(root, "config.yaml"), BilibiliConfig{
		Enabled:     true,
		CacheDir:    filepath.Join(root, "cache"),
		MusicDir:    filepath.Join(root, "music"),
		Ffmpeg:      "ffmpeg",
		SearchLimit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	query := os.Getenv("BILIBILI_LIVE_QUERY")
	if query == "" {
		query = "周杰伦 晴天"
	}
	items, err := service.search(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("没有搜到结果")
	}
	video, err := service.resolve(ctx, items[0].BVID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if video.AudioURL == "" || video.AudioBandwidth <= 0 {
		t.Fatalf("音轨信息不完整: %+v", video)
	}
	t.Logf("搜索=%q，首个=%s，音轨=%s %d kbps", items[0].Title, video.BVID, video.AudioCodec, video.AudioBandwidth/1000)
}

func TestBilibiliAudioPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	service, err := newBilibiliService(filepath.Join(root, "config.yaml"), BilibiliConfig{
		Enabled:  true,
		CacheDir: filepath.Join(root, "cache"),
		Ffmpeg:   "ffmpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(service.dir, "BV123-456.mp3")
	if err := os.WriteFile(valid, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.audioPath("BV123-456.mp3"); !ok {
		t.Fatal("valid cached audio not found")
	}
	if _, ok := service.audioPath("../BV123-456.mp3"); ok {
		t.Fatal("path traversal should be rejected")
	}
	if _, ok := service.audioPath("../../secret.txt"); ok {
		t.Fatal("non-mp3 path should be rejected")
	}
}

func TestBilibiliRecommendationPrefersSingleHighQualityTrack(t *testing.T) {
	items := []bilibiliSearchItem{
		{Title: "周杰伦 100首精选合集", DurationSeconds: 18000},
		{Title: "【Hi-Res无损】晴天 - 周杰伦", DurationSeconds: 270},
		{Title: "晴天 KTV 伴奏", DurationSeconds: 270},
	}
	ranked := rankBilibiliItems("周杰伦 晴天", items)
	if !ranked[0].Recommended || ranked[0].DurationSeconds != 270 {
		t.Fatalf("unexpected recommendation: %+v", ranked)
	}
}

func TestBilibiliWBISignature(t *testing.T) {
	mixinKey, err := bilibiliMixinKey(
		"7cd084941338484aae1ad9425b84077c",
		"4932caff0ff746eab6f01bf08b70ac45",
	)
	if err != nil {
		t.Fatal(err)
	}
	if mixinKey != "ea1db124af3c7062474693fa704f4ff8" {
		t.Fatalf("mixin key = %q", mixinKey)
	}
	signed := signBilibiliWBI(map[string][]string{
		"bvid":  {"BV1xx411c7mD"},
		"cid":   {"456"},
		"fnval": {"4048"},
		"fnver": {"0"},
		"fourk": {"1"},
		"qn":    {"0"},
	}, mixinKey, time.Unix(1700000000, 0))
	if got := signed.Get("wts"); got != "1700000000" {
		t.Fatalf("wts = %q", got)
	}
	if got := signed.Get("w_rid"); got != "1754735b8b98fcd3b9f7d1301462af70" {
		t.Fatalf("w_rid = %q", got)
	}
}

func TestBilibiliBuvidFingerprint(t *testing.T) {
	const want = "26dcaebdb34c5b77f7869e2eeea3cfd"
	if got := bilibiliBuvidFingerprint(bilibiliUserAgent, 31); got != want {
		t.Fatalf("buvid_fp = %q, want %q", got, want)
	}
}

func TestBilibiliAudioTracksAcceptObjectAndArray(t *testing.T) {
	var dash struct {
		Audio bilibiliAudioTracks `json:"audio"`
		Dolby struct {
			Audio bilibiliAudioTracks `json:"audio"`
		} `json:"dolby"`
		Flac struct {
			Audio bilibiliAudioTracks `json:"audio"`
		} `json:"flac"`
	}
	raw := `{
		"audio":[{"id":30280,"baseUrl":"https://example/aac","bandwidth":320000,"codecs":"mp4a.40.2"}],
		"dolby":{"audio":[{"id":30250,"baseUrl":"https://example/dolby","bandwidth":448000,"codecs":"ec-3"}]},
		"flac":{"audio":{"id":30251,"baseUrl":"https://example/flac","bandwidth":1500000,"codecs":"fLaC"}}
	}`
	if err := json.Unmarshal([]byte(raw), &dash); err != nil {
		t.Fatal(err)
	}
	if len(dash.Audio) != 1 || len(dash.Dolby.Audio) != 1 || len(dash.Flac.Audio) != 1 {
		t.Fatalf("unexpected tracks: %+v", dash)
	}
	if bilibiliAudioRank(dash.Flac.Audio[0]) <= bilibiliAudioRank(dash.Audio[0]) {
		t.Fatal("FLAC should rank above AAC")
	}
}

func TestBilibiliRequestCookieMergesAnonymousAndLogin(t *testing.T) {
	root := t.TempDir()
	service, err := newBilibiliService(filepath.Join(root, "config.yaml"), BilibiliConfig{
		Enabled:  true,
		CacheDir: filepath.Join(root, "cache"),
		MusicDir: filepath.Join(root, "music"),
		Cookie:   "SESSDATA=secret; bili_jct=csrf",
	})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://api.bilibili.com/")
	service.client.Jar.SetCookies(base, []*http.Cookie{
		{Name: "buvid3", Value: "device", Domain: ".bilibili.com", Path: "/"},
		{Name: "buvid_fp", Value: "fingerprint", Domain: ".bilibili.com", Path: "/"},
	})
	cookie := service.requestCookie("https://api.bilibili.com/x/web-interface/nav")
	for _, part := range []string{"SESSDATA=secret", "bili_jct=csrf", "buvid3=device", "buvid_fp=fingerprint"} {
		if !strings.Contains(cookie, part) {
			t.Fatalf("cookie %q missing %q", cookie, part)
		}
	}
}

func TestBilibiliSaveToPermanentMusicDir(t *testing.T) {
	root := t.TempDir()
	musicDir := filepath.Join(root, "music")
	service, err := newBilibiliService(filepath.Join(root, "config.yaml"), BilibiliConfig{
		Enabled:  true,
		CacheDir: filepath.Join(root, "cache"),
		MusicDir: musicDir,
		Ffmpeg:   "ffmpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(service.dir, "BV123-456.mp3")
	if err := os.WriteFile(cached, []byte("mp3-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, name, err := service.saveToMusic(bilibiliCachedAudio{
		Video: bilibiliVideo{BVID: "BV123", CID: 456, Title: "晴天 / 周杰伦"},
		Path:  cached,
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "晴天 _ 周杰伦 [BV123-456].mp3" {
		t.Fatalf("unexpected filename: %s", name)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "mp3-data" {
		t.Fatalf("saved data mismatch: %q, %v", data, err)
	}
}

func TestBilibiliSaveLosslessSourceAsFLAC(t *testing.T) {
	root := t.TempDir()
	musicDir := filepath.Join(root, "music")
	service, err := newBilibiliService(filepath.Join(root, "config.yaml"), BilibiliConfig{
		Enabled:  true,
		CacheDir: filepath.Join(root, "cache"),
		MusicDir: musicDir,
		Ffmpeg:   "ffmpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(service.dir, "BV123-456.flac")
	if err := os.WriteFile(source, []byte("flac-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, name, err := service.saveToMusic(bilibiliCachedAudio{
		Video:      bilibiliVideo{BVID: "BV123", CID: 456, Title: "晴天 / 周杰伦", AudioID: 30251, AudioCodec: "fLaC"},
		Path:       filepath.Join(service.dir, "BV123-456.mp3"),
		SourcePath: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "晴天 _ 周杰伦 [BV123-456].flac" {
		t.Fatalf("unexpected filename: %s", name)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "flac-data" {
		t.Fatalf("saved data mismatch: %q, %v", data, err)
	}
}
