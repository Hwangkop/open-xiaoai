package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"uuid"
)

// 豆包网页版「自动播报」使用的语音合成 WebSocket。
//
// 协议要点（逆向自 s2-doubao-speech-sdk）：
//   - 出站帧是 hand-rolled protobuf：f2=api_app_key, f3=namespace, f5=event, f6=payload(JSON), f8=session_id
//   - 入站帧同样是 protobuf：f4=event, f7=JSON 元信息, f8=音频分片
//   - 时序：StartTask -> TaskStarted -> StartSession -> SessionStarted
//     -> BidirectionalTTS{text} -> EndTTS -> TTSResponse*f8... -> TTSEnded
//
// 该端点目前不需要登录 Cookie，因此可以在服务端直接调用。
const (
	voiceGenieAppKey = "GOqQpfo1fO7slHv8"
	voiceGenieURL    = "wss://frontier-audio-web-ws.doubao.com/api/v2/sami/voicegenie" +
		"?api_app_key=" + voiceGenieAppKey +
		"&namespace=VoiceGenie&version_code=20800&language=zh" +
		"&device_platform=web&pkg_type=release_version&pc_version=3.43.2" +
		"&region=CN&sys_region=CN&samantha_web=1&use-olympus-account=1" +
		"&doubao_device_platform=web&aid=497858&real_aid=497858" +
		"&device_id=7701979379005720090&doubao_pc_version=3.43.2" +
		"&web_id=7701979381636335130&tea_uuid=7701979381636335130" +
		"&web_platform=browser&web_tab_id=open-xiaoai-chat"

	defaultDoubaoSpeaker    = "zh_female_taozi_conversation_v4_wvae_bigtts"
	defaultDoubaoFormat     = "mp3"
	defaultDoubaoBitRate    = 32000
	defaultDoubaoSampleRate = 24000
	defaultDoubaoSpeed      = 1.0
)

// DoubaoTTS 把文本合成为音频文件并缓存在本地，供音箱通过 HTTP 拉取播放。
type DoubaoTTS struct {
	speaker    string
	format     string
	bitRate    int
	sampleRate int
	speechRate float64
	pitch      float64
	emotion    string
	dir        string
}

// WithVoice 复制一份配置、换音色/情绪，用于管理台试听（不改全局配置）。
func (t *DoubaoTTS) WithVoice(speaker, emotion string) *DoubaoTTS {
	cp := *t
	if v := strings.TrimSpace(speaker); v != "" {
		cp.speaker = v
	}
	cp.emotion = strings.TrimSpace(emotion)
	return &cp
}

func NewDoubaoTTS(cfg TTSConfig) *DoubaoTTS {
	speaker := strings.TrimSpace(cfg.Speaker)
	if speaker == "" {
		speaker = defaultDoubaoSpeaker
	}
	format := strings.TrimSpace(cfg.Format)
	if format == "" {
		format = defaultDoubaoFormat
	}
	bitRate := cfg.BitRate
	if bitRate <= 0 {
		bitRate = defaultDoubaoBitRate
	}
	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = defaultDoubaoSampleRate
	}
	speechRate := cfg.SpeechRate
	if speechRate <= 0 {
		speechRate = defaultDoubaoSpeed
	}
	dir := defaultTTSDir()
	return &DoubaoTTS{
		speaker:    speaker,
		format:     format,
		bitRate:    bitRate,
		sampleRate: sampleRate,
		speechRate: speechRate,
		pitch:      cfg.Pitch,
		dir:        dir,
	}
}

// defaultTTSDir 是合成音频的缓存目录，HTTP 路由 /tts/ 也从这里读文件。
func defaultTTSDir() string {
	return filepath.Join(os.TempDir(), "open-xiaoai-tts")
}

func (t *DoubaoTTS) Dir() string { return t.dir }

// Synthesize 返回可直接给音箱播放的本地文件名（位于 Dir 内）。
func (t *DoubaoTTS) Synthesize(ctx context.Context, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("empty text")
	}
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return "", fmt.Errorf("create tts dir: %w", err)
	}

	name := t.cacheName(text)
	path := filepath.Join(t.dir, name)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return name, nil
	}

	audio, err := t.synthesizeWS(ctx, text)
	if err != nil {
		return "", err
	}
	if len(audio) == 0 {
		return "", errors.New("doubao tts returned empty audio")
	}

	// 先写临时文件再 rename，避免并发读到半个文件。
	tmp, err := os.CreateTemp(t.dir, ".tts-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(audio); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return name, nil
}

func (t *DoubaoTTS) cacheName(text string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d|%.2f|%.2f|%s",
		t.speaker, t.format, t.bitRate, t.sampleRate, t.speechRate, t.pitch, text)))
	return hex.EncodeToString(sum[:16]) + "." + t.format
}

func (t *DoubaoTTS) sessionPayload() string {
	ttsCfg := map[string]any{
		"speaker": t.speaker,
		"audio_config": map[string]any{
			"bit_rate":    t.bitRate,
			"format":      t.format,
			"sample_rate": t.sampleRate,
		},
	}
	// 情绪（部分音色支持）；服务端不认识时会忽略
	if t.emotion != "" {
		ttsCfg["emotion"] = t.emotion
	}
	if t.speechRate != defaultDoubaoSpeed || t.pitch != 0 {
		ttsCfg["extra"] = map[string]any{
			"post_process": map[string]any{
				"pitch":       t.pitch,
				"speech_rate": t.speechRate,
			},
		}
	}
	cfg := map[string]any{
		"business":            1,
		"request_type":        4,
		"enable_text_reading": true,
		"interrupt_type":      0,
		"tts":                 ttsCfg,
	}
	data, _ := json.Marshal(cfg)
	return string(data)
}

func (t *DoubaoTTS) synthesizeWS(ctx context.Context, text string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, voiceGenieURL, nil)
	if err != nil {
		return nil, fmt.Errorf("doubao tts dial: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	sessionID := uuid.New().String()
	send := func(event, payload string, withSid bool) error {
		sid := ""
		if withSid {
			sid = sessionID
		}
		return conn.Write(ctx, websocket.MessageBinary, encodeVoiceFrame(event, payload, sid))
	}

	if err := send("StartTask", "{}", false); err != nil {
		return nil, fmt.Errorf("send StartTask: %w", err)
	}

	sessionCfg := t.sessionPayload()
	textPayload, _ := json.Marshal(map[string]string{"text": text})
	var audio []byte
	textSent := false

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if len(audio) > 0 {
				return audio, nil
			}
			return nil, fmt.Errorf("doubao tts read: %w", err)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		event, payload, chunk := decodeVoiceFrame(data)
		if len(chunk) > 0 {
			audio = append(audio, chunk...)
		}
		switch event {
		case "TaskStarted":
			if err := send("StartSession", sessionCfg, true); err != nil {
				return nil, fmt.Errorf("send StartSession: %w", err)
			}
		case "SessionStarted":
			if !textSent {
				textSent = true
				if err := send("BidirectionalTTS", string(textPayload), true); err != nil {
					return nil, fmt.Errorf("send BidirectionalTTS: %w", err)
				}
				if err := send("EndTTS", "{}", true); err != nil {
					return nil, fmt.Errorf("send EndTTS: %w", err)
				}
			}
		case "TTSEnded":
			return audio, nil
		case "SessionFailed", "TaskFailed":
			return nil, fmt.Errorf("doubao tts failed: %s %s", event, payload)
		}
	}
}

// --- 极简 protobuf 编解码（只覆盖该协议用到的字段）---

func encodeVoiceFrame(event, payload, sessionID string) []byte {
	var out []byte
	out = appendVoiceField(out, 2, voiceGenieAppKey)
	out = appendVoiceField(out, 3, "VoiceGenie")
	out = appendVoiceField(out, 5, event)
	out = appendVoiceField(out, 6, payload)
	if sessionID != "" {
		out = appendVoiceField(out, 8, sessionID)
	}
	return out
}

func appendVoiceField(dst []byte, num int, value string) []byte {
	dst = append(dst, byte(num<<3|2))
	dst = appendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendUvarint(dst []byte, v uint64) []byte {
	for v > 127 {
		dst = append(dst, byte(v&127)|128)
		v >>= 7
	}
	return append(dst, byte(v))
}

func decodeVoiceFrame(data []byte) (event, payload string, audio []byte) {
	i := 0
	for i < len(data) {
		key, n := readUvarint(data[i:])
		if n <= 0 {
			return
		}
		i += n
		field, wire := int(key>>3), int(key&7)
		switch wire {
		case 0:
			_, n := readUvarint(data[i:])
			if n <= 0 {
				return
			}
			i += n
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			length, n := readUvarint(data[i:])
			if n <= 0 {
				return
			}
			i += n
			end := i + int(length)
			if end > len(data) {
				return
			}
			value := data[i:end]
			i = end
			switch field {
			case 4:
				event = string(value)
			case 7:
				payload = string(value)
			case 8:
				audio = value
			}
		default:
			return
		}
	}
	return
}

func readUvarint(data []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i := 0; i < len(data); i++ {
		b := data[i]
		v |= uint64(b&127) << shift
		if b&128 == 0 {
			return v, i + 1
		}
		shift += 7
		if shift >= 64 {
			return 0, -1
		}
	}
	return 0, -1
}
