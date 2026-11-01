package main

// /silence.wav：给音箱播一段"无声但有播放状态"的音频，
// 用来在 agent 思考时点亮播放灯（播放状态由 mediaplayer 驱动）。

import (
	"encoding/binary"
	"net/http"
	"time"
)

const (
	silenceRate    = 16000
	silenceSeconds = 300 // 最长 5 分钟，够一轮思考了
)

func (a *adminServer) handleSilence(w http.ResponseWriter, r *http.Request) {
	dataLen := silenceRate * silenceSeconds // 8-bit mono：1 字节/采样
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")

	// WAV 头
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+dataLen))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1) // mono
	binary.LittleEndian.PutUint32(hdr[24:], silenceRate)
	binary.LittleEndian.PutUint32(hdr[28:], silenceRate) // byte rate
	binary.LittleEndian.PutUint16(hdr[32:], 1)           // block align
	binary.LittleEndian.PutUint16(hdr[34:], 8)           // bits
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(dataLen))
	if _, err := w.Write(hdr); err != nil {
		return
	}

	// 8-bit PCM 的静音是 0x80
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = 0x80
	}
	flusher, _ := w.(http.Flusher)
	written := 0
	for written < dataLen {
		if r.Context().Err() != nil {
			return
		}
		n, err := w.Write(buf)
		if err != nil {
			return
		}
		written += n
		if flusher != nil {
			flusher.Flush()
		}
		// 稍微限速，避免瞬间推满
		time.Sleep(10 * time.Millisecond)
	}
}
