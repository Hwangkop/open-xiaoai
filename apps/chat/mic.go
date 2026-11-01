package main

// 服务端唤醒模式：接收音箱推上来的麦克风 PCM，做语音活动检测，切出一句句话。
//
// 音频格式：16kHz / mono / S16_LE（客户端 arecord -D noop 已转好）。
// 切出来的语音片段先落盘到 /tmp/xiaoai-mic/，方便离线验证识别效果；
// 后续接 ASR/唤醒词时，把 onUtterance 换成喂给识别器即可。

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	micSampleRate = 16000
	micFrameMs    = 20
	micFrameBytes = micSampleRate / 1000 * micFrameMs * 2 // 640

	// VAD 参数
	micRMSThreshold  = 1200 // int16 域的最低门限（实测环境底噪 RMS≈2000，需自适应抬高）
	micSpeechFrames  = 3    // 连续多少帧算开口（60ms）
	micSilenceFrames = 40   // 连续多少帧静音算说完（800ms）
	micMinUtterMs    = 320  // 太短的丢弃
	micMaxUtterMs    = 10000
	micPreRollFrames = 25 // 开口前回补 500ms
)

// MicListener 累积音频并切句。
type MicListener struct {
	mu sync.Mutex

	buf        []byte
	noiseFloor float64

	utter       []byte
	utterMaxRMS float64  // 本段内的峰值能量，用于过滤噪声
	preRoll     [][]byte // 开口前的音频，避免吃掉字头
	speechRun   int
	quietRun    int
	inSpeech    bool

	saveDir string
	saveSeq int

	// 诊断用：统计收到的字节和最近能量
	statBytes   int64
	statFrames  int64
	statLastLog time.Time
	statLastRMS float64

	onUtterance func(pcm []byte, wavPath string)

	// onSpeechStart 检测到用户开口时回调（用于点亮指示灯）。
	onSpeechStart func()

	// tap 原始 PCM 旁路（调试录制/AEC 测试用）。回调在锁外执行，避免磁盘 IO 卡住 VAD。
	tap func(pcm []byte)
}

func NewMicListener(saveDir string) *MicListener {
	if saveDir != "" {
		_ = os.MkdirAll(saveDir, 0o755)
	}
	return &MicListener{noiseFloor: 2000, saveDir: saveDir}
}

// SetTap 挂一个原始 PCM 旁路，返回取消函数。同一时刻只允许一个挂载者。
func (m *MicListener) SetTap(fn func(pcm []byte)) (stop func()) {
	m.mu.Lock()
	m.tap = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		m.tap = nil
		m.mu.Unlock()
	}
}

// SetSpeechStartHandler 设置"检测到开口"回调（比切句早 800ms，用来点亮指示灯）。
func (m *MicListener) SetSpeechStartHandler(fn func()) {
	m.mu.Lock()
	m.onSpeechStart = fn
	m.mu.Unlock()
}

// SetUtteranceHandler 设置切句回调（完整一段 16k/mono/S16_LE）。
func (m *MicListener) SetUtteranceHandler(fn func(pcm []byte, wavPath string)) {
	m.mu.Lock()
	m.onUtterance = fn
	m.mu.Unlock()
}

// Feed 喂入任意长度的 PCM，内部按 20ms 帧处理。
func (m *MicListener) Feed(pcm []byte) {
	m.mu.Lock()
	tap := m.tap
	m.mu.Unlock()
	if tap != nil {
		tap(pcm) // 锁外调用：录制回调可能做磁盘 IO
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.statBytes += int64(len(pcm))
	if m.statLastLog.IsZero() {
		m.statLastLog = time.Now()
	}
	if time.Since(m.statLastLog) >= 5*time.Second {
		log.Printf("🎧 mic 流: 累计 %.1f KB，%d 帧，最近 RMS=%.0f，噪声基线=%.0f",
			float64(m.statBytes)/1024, m.statFrames, m.statLastRMS, m.noiseFloor)
		m.statLastLog = time.Now()
	}

	m.buf = append(m.buf, pcm...)
	for len(m.buf) >= micFrameBytes {
		frame := make([]byte, micFrameBytes)
		copy(frame, m.buf[:micFrameBytes])
		m.buf = m.buf[micFrameBytes:]
		m.processFrame(frame)
	}
}

func (m *MicListener) processFrame(frame []byte) {
	rms := frameRMS(frame)
	m.statFrames++
	m.statLastRMS = rms

	// 噪声基线：始终缓慢跟踪（开口时也跟，但更慢），避免被持续噪声顶死
	if m.noiseFloor <= 0 {
		m.noiseFloor = rms
	} else if m.inSpeech {
		m.noiseFloor = m.noiseFloor*0.9995 + rms*0.0005
	} else {
		m.noiseFloor = m.noiseFloor*0.995 + rms*0.005
	}
	threshold := float64(micRMSThreshold)
	if adaptive := m.noiseFloor * 2.2; adaptive > threshold {
		threshold = adaptive
	}
	voice := rms > threshold

	if !m.inSpeech {
		// 滚动保留最近 500ms，开口时补回去，避免吃掉字头
		m.preRoll = append(m.preRoll, frame)
		if len(m.preRoll) > micPreRollFrames {
			m.preRoll = m.preRoll[1:]
		}
		if voice {
			m.speechRun++
		} else {
			m.speechRun = 0
		}
		if m.speechRun < micSpeechFrames {
			return
		}
		// 开口
		m.inSpeech = true
		m.quietRun = 0
		m.utter = m.utter[:0]
		m.utterMaxRMS = 0
		for _, f := range m.preRoll {
			m.utter = append(m.utter, f...)
		}
		m.preRoll = m.preRoll[:0]
		if m.onSpeechStart != nil {
			fn := m.onSpeechStart
			go fn() // 不要在锁里跑外部回调
		}
	}

	m.utter = append(m.utter, frame...)
	if rms > m.utterMaxRMS {
		m.utterMaxRMS = rms
	}
	if voice {
		m.quietRun = 0
	} else {
		m.quietRun++
	}

	maxBytes := micMaxUtterMs / 1000 * micSampleRate * 2
	if m.quietRun >= micSilenceFrames || len(m.utter) >= maxBytes {
		m.flushLocked()
	}
}

func frameRMS(frame []byte) float64 {
	n := len(frame) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(frame[i*2:])))
		sum += s * s
	}
	return math.Sqrt(sum / float64(n))
}

func (m *MicListener) flushLocked() {
	durMs := len(m.utter) / 2 * 1000 / micSampleRate
	pcm := m.utter
	m.utter = nil
	m.inSpeech = false
	m.speechRun = 0
	m.quietRun = 0

	if durMs < micMinUtterMs {
		return
	}
	// 噪声门：峰值必须显著高于底噪，否则就是沙沙声/风扇声
	if floor := m.noiseFloor; floor > 0 && m.utterMaxRMS < floor*2.2 {
		log.Printf("🔕 噪声片段（峰值 %.0f / 底噪 %.0f），丢弃", m.utterMaxRMS, floor)
		return
	}
	// 语音形态检查：人说话能量有起伏（CV 高），过零率中等；
	// 稳态噪声（风扇/空调）CV 很低，白噪声过零率极高。
	if cv, zcr, ok := speechShape(pcm); ok {
		if cv < micMinCV || zcr > micMaxZCR {
			log.Printf("🔕 不像人说话（起伏 %.2f / 过零率 %.2f），丢弃", cv, zcr)
			return
		}
	}

	log.Printf("🎤 检测到语音: %d ms（峰值 %.0f / 底噪 %.0f）", durMs, m.utterMaxRMS, m.noiseFloor)

	wavPath := ""
	if m.saveDir != "" {
		m.saveSeq++
		wavPath = filepath.Join(m.saveDir,
			fmt.Sprintf("%s-%02d.wav", time.Now().Format("150405"), m.saveSeq))
		if err := writeWAV(wavPath, pcm, micSampleRate); err != nil {
			log.Printf("⚠️ 保存语音失败: %v", err)
			wavPath = ""
		}
	}
	if fn := m.onUtterance; fn != nil {
		go fn(pcm, wavPath)
	}
}

// writeWAV 写 16bit 单声道 WAV，便于离线试听/验证。
func writeWAV(path string, pcm []byte, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dataLen := uint32(len(pcm))
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36+dataLen)
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], dataLen)

	if _, err := f.Write(hdr); err != nil {
		return err
	}
	_, err = f.Write(pcm)
	return err
}

// 语音形态阈值（实测：人声 CV 0.47~0.83、过零率 0.06~0.13；
// 静音 CV 0、白噪声过零率 0.5）。
const (
	micMinCV  = 0.25
	micMaxZCR = 0.35
)

// speechShape 计算片段的分帧能量起伏（CV）和过零率。
func speechShape(pcm []byte) (cv, zcr float64, ok bool) {
	frame := micSampleRate / 50 // 20ms
	samples := len(pcm) / 2
	if samples < frame*5 {
		return 0, 0, false
	}
	at := func(i int) int16 { return int16(binary.LittleEndian.Uint16(pcm[i*2:])) }
	var sum, sumSq float64
	n := 0
	for start := 0; start+frame <= samples; start += frame {
		var e float64
		for j := start; j < start+frame; j++ {
			v := float64(at(j)) / 32768.0
			e += v * v
		}
		rms := math.Sqrt(e / float64(frame))
		sum += rms
		sumSq += rms * rms
		n++
	}
	if n == 0 {
		return 0, 0, false
	}
	mean := sum / float64(n)
	variance := sumSq/float64(n) - mean*mean
	if variance < 0 {
		variance = 0
	}
	if mean > 0 {
		cv = math.Sqrt(variance) / mean
	}
	crossings := 0
	for i := 1; i < samples; i++ {
		if (at(i-1) < 0) != (at(i) < 0) {
			crossings++
		}
	}
	zcr = float64(crossings) / float64(samples-1)
	return cv, zcr, true
}
