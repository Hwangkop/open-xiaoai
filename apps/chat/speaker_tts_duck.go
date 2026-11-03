package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const defaultTTSMusicDuckPercent = 50

// TTSMusicDuckConfig 与 music_duck 分离：后者贯穿音乐会话，这里只覆盖整轮播报。
type TTSMusicDuckConfig struct {
	Enabled bool `yaml:"enabled"`
	Percent int  `yaml:"percent"` // 播报时保留的音乐音量比例，1-100；不是听感响度比例。
}

type ttsDuckReplyKey struct{}

type ttsDuckReply struct {
	speaker                     *Speaker
	started, closed, suppressed bool
	playing                     int
}

type ttsMusicDuckRecord struct {
	Original int `json:"original"`
	Applied  int `json:"applied"`
}

// 全部由 musicVolumeMu 保护；播放中的引用必须等 RPC 返回才释放。
type ttsMusicDuckState struct {
	enabled bool
	percent int
	file    string
	replies map[*ttsDuckReply]struct{}
	record  *ttsMusicDuckRecord
}

func (s *Speaker) SetTTSMusicDuckFile(path string) {
	s.musicVolumeMu.Lock()
	defer s.musicVolumeMu.Unlock()
	s.ttsMusicDuck.file = path
}

// SetTTSMusicDuck 支持热关闭；关闭时只恢复仍由我们持有的音量。
func (s *Speaker) SetTTSMusicDuck(cfg TTSMusicDuckConfig) {
	s.musicVolumeMu.Lock()
	defer s.musicVolumeMu.Unlock()
	if cfg.Percent < 1 {
		cfg.Percent = defaultTTSMusicDuckPercent
	}
	if cfg.Percent > 100 {
		cfg.Percent = 100
	}
	s.ttsMusicDuck.enabled, s.ttsMusicDuck.percent = cfg.Enabled, cfg.Percent
	if !cfg.Enabled {
		s.suppressTTSDuckRepliesLocked()
		s.restoreTTSDuckLocked()
	}
}

// WithTTSDuckReply 创建整轮范围。登记时不改音量，首句实际播放时才开始压低。
// 继承已有范围，让回复前缀、流水线及错误提示共用同一次保存/恢复。
func (s *Speaker) WithTTSDuckReply(ctx context.Context) (context.Context, func()) {
	if r, ok := ctx.Value(ttsDuckReplyKey{}).(*ttsDuckReply); ok && r.speaker == s {
		return ctx, func() {}
	}
	r := &ttsDuckReply{speaker: s}
	s.musicVolumeMu.Lock()
	if s.ttsMusicDuck.replies == nil {
		s.ttsMusicDuck.replies = make(map[*ttsDuckReply]struct{})
	}
	s.ttsMusicDuck.replies[r] = struct{}{}
	s.musicVolumeMu.Unlock()
	var once sync.Once
	return context.WithValue(ctx, ttsDuckReplyKey{}, r), func() {
		once.Do(func() {
			s.musicVolumeMu.Lock()
			defer s.musicVolumeMu.Unlock()
			r.closed = true
			if r.playing == 0 {
				delete(s.ttsMusicDuck.replies, r)
			}
			s.finishTTSDuckLocked()
		})
	}
}

func (s *Speaker) beginTTSDuckPlayback(ctx context.Context) (func(), error) {
	s.musicVolumeMu.Lock()
	defer s.musicVolumeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, ok := ctx.Value(ttsDuckReplyKey{}).(*ttsDuckReply)
	if !ok || r.speaker != s || r.closed {
		return nil, fmt.Errorf("播报音量会话已结束")
	}
	r.playing++
	if !r.suppressed && s.ttsMusicDuck.enabled && s.MusicPlaying() {
		if !r.started && !s.hasTTSDuckOwnerLocked() {
			// 上次恢复失败的状态先补还原，不能把临时低音量当成本轮原值。
			if !s.restorePendingTTSDuckLocked() {
				r.suppressed = true
			}
		}
		r.started = true
		if !r.suppressed && s.ttsMusicDuck.record == nil {
			if !s.applyTTSDuckLocked() {
				r.suppressed = true
			}
		} else if !r.suppressed {
			// 按键/其他控制端可能改过音量：一旦观察到变化，本轮交还控制权。
			if cur, err := s.volume(); err == nil && cur != s.ttsMusicDuck.record.Applied {
				s.abandonTTSDuckLocked()
			}
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.musicVolumeMu.Lock()
			defer s.musicVolumeMu.Unlock()
			r.playing--
			if r.closed && r.playing == 0 {
				delete(s.ttsMusicDuck.replies, r)
			}
			s.finishTTSDuckLocked()
		})
	}, nil
}

func (s *Speaker) hasTTSDuckOwnerLocked() bool {
	for r := range s.ttsMusicDuck.replies {
		if r.started && !r.suppressed && (!r.closed || r.playing > 0) {
			return true
		}
	}
	return false
}

func (s *Speaker) finishTTSDuckLocked() {
	if !s.hasTTSDuckOwnerLocked() {
		s.restoreTTSDuckLocked()
	}
}

func (s *Speaker) applyTTSDuckLocked() bool {
	cur, err := s.volume()
	if err != nil || cur < 0 || cur > 100 {
		log.Printf("⚠️ [TTS 压低音乐] 无法读取有效音量，跳过: %v", err)
		return false
	}
	percent := s.ttsMusicDuck.percent
	if percent < 1 {
		percent = defaultTTSMusicDuckPercent
	}
	next := cur * percent / 100
	if cur > 0 && next < 1 {
		next = 1
	}
	if next >= cur {
		return false // 静音/极低音量保持原样，本轮后续句子也不重新压低。
	}
	rec := &ttsMusicDuckRecord{Original: cur, Applied: next}
	if err := s.saveTTSDuckRecordLocked(rec); err != nil {
		log.Printf("⚠️ [TTS 压低音乐] 无法保存恢复记录，跳过: %v", err)
		return false
	}
	// 写入超时可能已在设备执行，保留记录，并在结束/重连后核对恢复。
	s.ttsMusicDuck.record = rec
	if err := s.setVolume(next); err != nil {
		log.Printf("⚠️ [TTS 压低音乐] 设置失败，保留恢复记录: %v", err)
		return false
	}
	log.Printf("🔉 [TTS 压低音乐] %d → %d", cur, next)
	return true
}

// restoreTTSDuckLocked 先核对实际音量，读取失败时绝不盲写。
func (s *Speaker) restoreTTSDuckLocked() bool {
	rec := s.ttsMusicDuck.record
	if rec == nil {
		return true
	}
	cur, err := s.volume()
	if err != nil {
		log.Printf("⚠️ [TTS 压低音乐] 无法核对恢复音量，留待重试: %v", err)
		return false
	}
	if cur == rec.Applied {
		if err := s.setVolume(rec.Original); err != nil {
			log.Printf("⚠️ [TTS 压低音乐] 恢复失败，留待重试: %v", err)
			return false
		}
		log.Printf("🔊 [TTS 压低音乐] 恢复为 %d", rec.Original)
	}
	s.ttsMusicDuck.record = nil
	s.clearTTSDuckRecordLocked()
	return true
}

func (s *Speaker) suppressTTSDuckRepliesLocked() {
	for r := range s.ttsMusicDuck.replies {
		r.suppressed = true
	}
}

// abandonTTSDuckLocked 仅在成功的手动音量写入/已观察到外部改动后调用。
func (s *Speaker) abandonTTSDuckLocked() {
	s.suppressTTSDuckRepliesLocked()
	s.ttsMusicDuck.record = nil
	s.clearTTSDuckRecordLocked()
}

func (s *Speaker) saveTTSDuckRecordLocked(rec *ttsMusicDuckRecord) error {
	path := s.ttsMusicDuck.file
	if path == "" {
		return fmt.Errorf("未设置音量恢复记录路径")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tts-music-duck-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func (s *Speaker) clearTTSDuckRecordLocked() {
	if path := s.ttsMusicDuck.file; path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("⚠️ [TTS 压低音乐] 清理恢复记录失败: %v", err)
		}
	}
}

// RestorePendingTTSMusicDuck 必须先于音乐会话层恢复；活跃播报在重连时继续持有。
func (s *Speaker) RestorePendingTTSMusicDuck() bool {
	s.musicVolumeMu.Lock()
	defer s.musicVolumeMu.Unlock()
	return s.restorePendingTTSDuckLocked()
}

func (s *Speaker) restorePendingTTSDuckLocked() bool {
	if s.hasTTSDuckOwnerLocked() {
		return false
	}
	if s.ttsMusicDuck.record == nil && s.ttsMusicDuck.file != "" {
		raw, err := os.ReadFile(s.ttsMusicDuck.file)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("⚠️ [TTS 压低音乐] 读取恢复记录失败: %v", err)
				return false
			}
			return true
		}
		var rec ttsMusicDuckRecord
		if json.Unmarshal(raw, &rec) != nil || rec.Original > 100 || rec.Applied < 0 || rec.Original <= rec.Applied {
			log.Printf("⚠️ [TTS 压低音乐] 恢复记录无效，不改设备音量")
			s.clearTTSDuckRecordLocked()
			return true
		}
		s.ttsMusicDuck.record = &rec
	}
	return s.restoreTTSDuckLocked()
}
