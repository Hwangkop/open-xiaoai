package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"time"
)

// 与网易云共用 music.opMu、播放完成回调及管理台控制，目录只保存元信息。
type bilibiliQueue struct {
	Name   string                `json:"name"`
	Items  []bilibiliCatalogItem `json:"items"`
	Index  int                   `json:"index"`
	Status string                `json:"status"`
	URL    string                `json:"url,omitempty"`
	Error  string                `json:"error,omitempty"`
}

// 只能在持有 music.mu 时调用；新音乐、听书和停止均撤销旧下载的起播资格。
func (m *musicState) cancelBilibiliLocked(clear bool) {
	if m.bilibiliCancel != nil {
		m.bilibiliCancel()
		m.bilibiliCancel = nil
	}
	m.bilibiliGeneration++
	if clear {
		m.bilibili = nil
	}
}

func (a *adminServer) cancelBilibili(clear bool, status string) {
	a.music.mu.Lock()
	a.music.cancelBilibiliLocked(clear)
	if a.music.bilibili != nil {
		a.music.bilibili.Status = status
	}
	a.music.mu.Unlock()
}

func (a *adminServer) requestBilibiliQueue(name string, items []bilibiliCatalogItem, index int, shuffle bool) (string, error) {
	if a.bilibili == nil {
		return "", fmt.Errorf("B 站音乐未启用")
	}
	if len(items) == 0 || index < 0 || index >= len(items) {
		return "", fmt.Errorf("视频序号超出目录范围")
	}
	items = append([]bilibiliCatalogItem(nil), items...)
	if shuffle {
		rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
		index = 0
	}
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	return a.scheduleBilibiliLocked(name, items, index)
}

// 音轨获取和转换不持有媒体操作锁。先暂停旧媒体，完成后重新检查轮次再下发。
func (a *adminServer) scheduleBilibiliLocked(name string, items []bilibiliCatalogItem, index int) (string, error) {
	if index < 0 || index >= len(items) {
		return "", fmt.Errorf("已经到播放队列边界")
	}
	if a.mediaBaseURL == nil || a.playURL == nil || strings.TrimSpace(a.mediaBaseURL()) == "" {
		return "", fmt.Errorf("音箱播放通道未连接")
	}
	if a.speaker != nil && a.speaker.MusicPlaying() {
		if err := a.speaker.PauseMusic(); err != nil {
			return "", err
		}
		// 目录准备期间不允许对话收尾恢复上一首；新音轨起播会创建新的播放轮次。
		a.speaker.stMu.Lock()
		a.speaker.st.music = false
		a.speaker.stMu.Unlock()
	}
	a.audiobooks.relinquishLocked()
	a.music.mu.Lock()
	a.music.cancelBilibiliLocked(false)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	a.music.bilibiliCancel = cancel
	generation := a.music.bilibiliGeneration
	a.music.bilibili = &bilibiliQueue{Name: name, Items: append([]bilibiliCatalogItem(nil), items...), Index: index, Status: "preparing"}
	a.music.mu.Unlock()
	a.persistMusic()
	item := items[index]
	go a.prepareBilibiliItem(ctx, cancel, generation, item)
	log.Printf("📺 [B站队列] 准备《%s》第 %d/%d 项", name, index+1, len(items))
	return fmt.Sprintf("正在准备 B 站音频《%s》，共 %d 项；音轨就绪且本轮回答结束后播放", item.Title, len(items)), nil
}

func (a *adminServer) prepareBilibiliItem(ctx context.Context, cancel context.CancelFunc, generation uint64, item bilibiliCatalogItem) {
	defer cancel()
	cached, err := a.bilibili.prepare(ctx, item.BVID, item.CID)
	if err != nil {
		a.finishBilibiliPreparation(generation, err)
		return
	}
	// 语音确认、原生识别结束后再起播；停止/切换使 context 立即失效。
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			a.finishBilibiliPreparation(generation, ctx.Err())
			return
		}
		a.music.opMu.Lock()
		a.music.mu.Lock()
		q := a.music.bilibili
		valid := q != nil && a.music.bilibiliGeneration == generation && q.Status == "preparing"
		a.music.mu.Unlock()
		if !valid {
			a.music.opMu.Unlock()
			return
		}
		if (a.engine != nil && a.engine.activeDialog.Load() != 0) || (a.speaker != nil && (a.speaker.Speaking() || a.speaker.NativeASRWindow() || a.speaker.TakeoverActive())) {
			a.music.opMu.Unlock()
			select {
			case <-ctx.Done():
				a.finishBilibiliPreparation(generation, ctx.Err())
				return
			case <-ticker.C:
				continue
			}
		}
		base := strings.TrimRight(a.mediaBaseURL(), "/")
		if base == "" {
			err = fmt.Errorf("音箱已断开连接")
		} else {
			u := base + "/bilibili/audio/" + cached.FileName
			err = a.playURL(u)
			if err == nil {
				a.music.mu.Lock()
				q.Status, q.URL, q.Error = "playing", u, ""
				q.Items[q.Index].CID = cached.Video.CID
				q.Items[q.Index].Duration = cached.Video.Duration
				a.music.bilibiliCancel = nil
				a.music.mu.Unlock()
				if a.speaker != nil {
					a.speaker.setMusicDuration(cached.Video.Duration * 1000)
				}
				a.persistMusic()
				log.Printf("📺 [B站队列] 已起播《%s》", item.Title)
			}
		}
		a.music.opMu.Unlock()
		if err != nil {
			a.finishBilibiliPreparation(generation, err)
		}
		return
	}
}

func (a *adminServer) finishBilibiliPreparation(generation uint64, err error) {
	a.music.opMu.Lock()
	defer a.music.opMu.Unlock()
	a.music.mu.Lock()
	q := a.music.bilibili
	if q == nil || a.music.bilibiliGeneration != generation {
		a.music.mu.Unlock()
		return
	}
	q.Status, q.Error = "error", err.Error()
	a.music.bilibiliCancel = nil
	a.music.mu.Unlock()
	a.persistMusic()
	log.Printf("⚠️ [B站队列] 准备失败：%v", err)
}

func (a *adminServer) controlBilibiliLocked(action string, index *int) (bool, error) {
	a.music.mu.Lock()
	q := a.music.bilibili
	if q == nil {
		a.music.mu.Unlock()
		return false, nil
	}
	copyQ := *q
	a.music.mu.Unlock()
	if action == "pause" || action == "stop" {
		if a.speaker != nil && a.speaker.MusicPlaying() && action == "pause" {
			if err := a.speaker.PauseMusic(); err != nil {
				return true, err
			}
		}
		status := "paused"
		if action == "stop" {
			status = "stopped"
		}
		a.cancelBilibili(false, status)
		a.persistMusic()
		return true, nil
	}
	if action == "resume" && copyQ.URL != "" && a.speaker != nil && a.speaker.MusicPlaying() && a.speaker.stateSnapshot().musicURL == copyQ.URL {
		if err := a.speaker.ResumeMusic(); err != nil {
			return true, err
		}
		a.music.mu.Lock()
		q.Status, q.Error = "playing", ""
		a.music.mu.Unlock()
		a.persistMusic()
		return true, nil
	}
	i := copyQ.Index
	switch action {
	case "next":
		i++
	case "previous":
		i--
	case "select":
		if index == nil {
			return true, fmt.Errorf("请选择视频序号")
		}
		i = *index
	case "resume", "replay":
	default:
		return true, fmt.Errorf("不支持的 B 站队列操作")
	}
	if i < 0 || i >= len(copyQ.Items) {
		return true, fmt.Errorf("已经到 B 站队列边界")
	}
	_, err := a.scheduleBilibiliLocked(copyQ.Name, copyQ.Items, i)
	return true, err
}

// 自然结束只推进 B 站自己的队列；准备、暂停、失败均不落回旧网易云歌单。
func (a *adminServer) nextBilibiliLocked() (bool, string, error) {
	a.music.mu.Lock()
	q := a.music.bilibili
	if q == nil {
		a.music.mu.Unlock()
		return false, "", nil
	}
	name, index, items := q.Name, q.Index, q.Items
	a.music.mu.Unlock()
	if index+1 >= len(items) {
		// 手动在最后一项按“下一项”不能把尚在播放的音轨标记为播完。
		if a.speaker == nil || a.speaker.MusicPlayback().DeviceStatus == 3 || a.speaker.MusicPlayback().DeviceStatus == 0 {
			a.music.mu.Lock()
			q.Status = "ended"
			a.music.mu.Unlock()
			a.persistMusic()
		}
		return true, "", fmt.Errorf("B 站队列《%s》已经播完", name)
	}
	msg, err := a.scheduleBilibiliLocked(name, items, index+1)
	return true, msg, err
}
