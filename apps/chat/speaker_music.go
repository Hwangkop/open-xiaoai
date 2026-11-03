package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// musicSessionState 由 stMu 保护；generation 使慢 RPC 与旧自动切歌失效。
type musicSessionState struct {
	generation     uint64
	status         string
	paused         bool
	deviceStatus   int
	observedAt     time.Time
	graceUntil     time.Time
	positionMs     int64
	durationMs     int64
	idleTicks      int
	lastError      string
	estimateHeldAt time.Time
}

type musicPlayback struct {
	Status            string `json:"status"`
	Tracking          bool   `json:"tracking"`
	DeviceStatus      int    `json:"device_status"`
	ObservedAt        int64  `json:"observed_at"`
	PositionMs        int64  `json:"position_ms"`
	DurationMs        int64  `json:"duration_ms"`
	PositionEstimated bool   `json:"position_estimated"`
	Error             string `json:"error,omitempty"`
}

func (s *Speaker) MusicPlayback() musicPlayback {
	s.stMu.Lock()
	m := s.st.musicSession
	active, restoring, speaking, native := s.st.music, s.st.restoring, s.st.speaking, s.st.nativeASR
	start, tracking := s.st.musicStart, s.st.musicTracking
	s.stMu.Unlock()
	status := m.status
	if status == "" {
		status = "stopped"
	}
	if active && !m.paused {
		switch {
		case restoring:
			status = "recovering"
		case speaking || native || s.TakeoverActive():
			status = "interrupted"
		case m.lastError != "" || (!m.observedAt.IsZero() && time.Since(m.observedAt) > 10*time.Second):
			status = "unknown"
		}
	}
	position, estimated := m.positionMs, false
	if position == 0 && active && !start.IsZero() {
		position, estimated = time.Since(start).Milliseconds(), true
		if !m.estimateHeldAt.IsZero() {
			position = m.estimateHeldAt.Sub(start).Milliseconds()
		}
	}
	if m.durationMs > 0 && position > m.durationMs {
		position = m.durationMs
	}
	observed := int64(0)
	if !m.observedAt.IsZero() {
		observed = m.observedAt.Unix()
	}
	return musicPlayback{status, tracking, m.deviceStatus, observed, position, m.durationMs, estimated, m.lastError}
}

func (s *Speaker) musicGenerationActive(generation uint64) bool {
	if s.Speaking() || s.NativeASRWindow() || s.TakeoverActive() || s.isRestoring() {
		return false
	}
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return s.st.music && !s.st.musicSession.paused && s.st.musicSession.generation == generation
}

func (s *Speaker) setMusicDuration(ms int) {
	s.stMu.Lock()
	s.st.musicSession.durationMs = int64(ms)
	s.stMu.Unlock()
}

func (s *Speaker) setMusicError(message string) {
	s.stMu.Lock()
	s.st.musicSession.lastError = message
	s.stMu.Unlock()
}

func (s *Speaker) pollMusic() {
	s.stMu.Lock()
	active, generation := s.st.music, s.st.musicSession.generation
	s.stMu.Unlock()
	if !active {
		return
	}
	if s.Speaking() || s.NativeASRWindow() || s.TakeoverActive() || s.isRestoring() {
		s.stMu.Lock()
		s.st.musicSession.idleTicks = 0
		s.stMu.Unlock()
		return
	}
	res, err := s.runShell("ubus -t 2 call mediaplayer player_get_play_status", 3000)
	status := -1
	if err == nil && res != nil && res.ExitCode == 0 {
		status = mediaPlayerStatus(res.Stdout)
	}
	// RPC 返回前可能已经换歌、停止或开始对话，旧结果不能影响新状态。
	if s.Speaking() || s.NativeASRWindow() || s.TakeoverActive() {
		return
	}
	s.stMu.Lock()
	m := &s.st.musicSession
	if !s.st.music || m.generation != generation || s.st.restoring {
		s.stMu.Unlock()
		return
	}
	if status < 0 || status > 3 {
		m.idleTicks = 0
		m.lastError = "暂时无法读取音箱播放状态，正在重试"
		s.stMu.Unlock()
		return
	}
	if m.deviceStatus != status {
		log.Printf("🎵 [音乐管理] 音箱状态 %d → %d（播放轮次 %d）", m.deviceStatus, status, generation)
	}
	m.deviceStatus, m.observedAt, m.lastError = status, time.Now(), ""
	if info, ok := mediaPlayerInfo(res.Stdout); ok {
		var detail struct {
			Position int64 `json:"position"`
		}
		if json.Unmarshal(info["play_song_detail"], &detail) == nil && detail.Position > 0 {
			m.positionMs = detail.Position
		}
	}
	if time.Now().Before(m.graceUntil) {
		m.idleTicks = 0
		s.stMu.Unlock()
		return
	}
	ended := false
	switch status {
	case 1:
		if !m.estimateHeldAt.IsZero() {
			s.st.musicStart = s.st.musicStart.Add(time.Since(m.estimateHeldAt))
			m.estimateHeldAt = time.Time{}
		}
		m.status, m.paused, m.idleTicks = "playing", false, 0
	case 2:
		// 暂停保留队列与监控，设备按键恢复后仍能自动切歌。
		if m.estimateHeldAt.IsZero() {
			m.estimateHeldAt = time.Now()
		}
		m.status, m.paused, m.idleTicks = "paused", true, 0
	case 0, 3:
		// 部分固件播完回报 0；只有设备确认的位置接近曲尾才允许推进。
		// 不用墙钟猜测结束，避免长对话/暂停导致误跳歌。
		ended = !m.paused && (status == 3 || (m.durationMs > 0 && m.positionMs > 0 && m.positionMs >= m.durationMs-2500))
		if ended {
			m.idleTicks++
		} else {
			m.idleTicks = 0
			if !m.paused {
				m.status = "stalled"
			}
		}
	}
	advance := ended && m.idleTicks >= musicEndIdleTicks
	s.stMu.Unlock()
	if !advance {
		return
	}
	if next := s.musicEndedHandler(); next != nil && next(generation) {
		return
	}
	if s.Speaking() || s.NativeASRWindow() || s.TakeoverActive() || s.isRestoring() {
		return
	}
	s.musicActionMu.Lock()
	defer s.musicActionMu.Unlock()
	s.stMu.Lock()
	if s.st.musicSession.generation != generation || s.st.musicSession.paused {
		s.stMu.Unlock()
		return
	}
	s.st.music = false
	s.st.musicSession.status = "ended"
	if s.st.musicSession.lastError != "" {
		s.st.musicSession.status = "error"
	}
	s.stMu.Unlock()
	s.RestoreMusicVolume()
	log.Printf("🎵 [音乐管理] 队列播放结束")
}

func (s *Speaker) PauseMusic() error {
	s.musicActionMu.Lock()
	defer s.musicActionMu.Unlock()
	s.stMu.Lock()
	active := s.st.music
	s.stMu.Unlock()
	if !active {
		return fmt.Errorf("当前没有可暂停的音乐")
	}
	res, err := s.runShell("mphelper pause", 3000)
	if err != nil {
		return err
	}
	if res == nil || res.ExitCode != 0 {
		return fmt.Errorf("暂停音乐失败")
	}
	s.stMu.Lock()
	s.st.musicSession.paused = true
	s.st.musicSession.status = "paused"
	s.st.musicSession.idleTicks = 0
	s.st.musicSession.generation++
	if s.st.musicSession.estimateHeldAt.IsZero() {
		s.st.musicSession.estimateHeldAt = time.Now()
	}
	s.stMu.Unlock()
	return nil
}

func (s *Speaker) ResumeMusic() error {
	s.stMu.Lock()
	if !s.st.music || s.st.musicURL == "" {
		s.stMu.Unlock()
		return fmt.Errorf("请先选择一首歌曲")
	}
	s.st.musicSession.paused = false
	if held := s.st.musicSession.estimateHeldAt; !held.IsZero() {
		s.st.musicStart = s.st.musicStart.Add(time.Since(held))
		s.st.musicSession.estimateHeldAt = time.Time{}
	}
	s.st.musicSession.idleTicks = 0
	s.st.musicSession.generation++
	s.stMu.Unlock()
	s.resumeMusicAfterSpeech()
	res, err := s.runShell("ubus -t 2 call mediaplayer player_get_play_status", 3000)
	if err != nil {
		return err
	}
	if res == nil || res.ExitCode != 0 || mediaPlayerStatus(res.Stdout) != 1 {
		return fmt.Errorf("音乐尚未恢复，可尝试重新播放当前歌曲")
	}
	return nil
}
