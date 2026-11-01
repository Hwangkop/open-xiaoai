package main

// 豆包流式语音识别（火山 v3 大模型流式，WebSocket）。
//
// 与 doubao_asr.go（整段提交 + 轮询）相比：
//   - 音频边发边识别，最后一段发完约 200ms 就拿到最终文本（整段式要 1.3s+）
//   - 但握手每句一次（TLS ~150ms），所以适合"说完就要结果"的短句
//
// 协议（v3 streaming，二进制帧）：
//   头 4 字节：[版本<<4|头长] [消息类型<<4|flags] [序列化<<4|压缩] [0]
//   完整请求：[头][body_len(4,BE)][JSON]
//   音频帧：  [头(类型2,flags0)][body_len(4,BE)][PCM]      ← 注意没有 sequence
//   结束帧：  [头(类型2,flags=0b0010)][body_len=0]
//   服务端响应：[头][sequence(4,BE)][body_len(4,BE)][JSON]  ← 有 sequence
//   flags=0b0011 或 sequence<0 表示最终结果。

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	doubaoStreamASRURL      = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel"
	doubaoStreamASRResource = "volc.bigasr.sauc.duration"

	// 消息类型
	saucMsgFullClient = 0b0001
	saucMsgAudioOnly  = 0b0010
	saucMsgError      = 0b1111

	saucFlagFinal = 0b0011 // 服务端最终结果
)

type DoubaoStreamASR struct {
	apiKey     string
	resourceID string
}

func NewDoubaoStreamASR(apiKey, resourceID string) *DoubaoStreamASR {
	if strings.TrimSpace(resourceID) == "" {
		resourceID = doubaoStreamASRResource
	}
	return &DoubaoStreamASR{apiKey: strings.TrimSpace(apiKey), resourceID: resourceID}
}

func (a *DoubaoStreamASR) Enabled() bool { return a.apiKey != "" }

func saucHeader(msgType, flags, serial uint8) []byte {
	return []byte{(1 << 4) | 1, (msgType << 4) | (flags & 0x0f), (serial << 4) | 0, 0}
}

// Transcribe 把整段 PCM 以流式方式发过去，返回最终文本。
// 若 onPartial 非空，每收到一次增量文本都会回调（可用于"边说边判唤醒词"）。
func (a *DoubaoStreamASR) Transcribe(ctx context.Context, pcm []byte, onPartial func(string)) (string, error) {
	return a.transcribe(ctx, pcm, onPartial, true)
}

// TranscribeFinalOnly 只返回最终文本。
func (a *DoubaoStreamASR) TranscribeFinalOnly(ctx context.Context, pcm []byte) (string, error) {
	return a.transcribe(ctx, pcm, nil, false)
}

func (a *DoubaoStreamASR) transcribe(ctx context.Context, pcm []byte, onPartial func(string), _ bool) (string, error) {
	if !a.Enabled() {
		return "", fmt.Errorf("未配置豆包 ASR 的 api_key")
	}
	if len(pcm) == 0 {
		return "", nil
	}

	start := time.Now()
	hdr := http.Header{}
	hdr.Set("X-Api-Key", a.apiKey)
	hdr.Set("X-Api-Resource-Id", a.resourceID)
	hdr.Set("X-Api-Connect-Id", newUUID())

	conn, _, err := websocket.Dial(ctx, doubaoStreamASRURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return "", fmt.Errorf("连接流式 ASR 失败: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(8 << 20)

	// 1) 初始化
	init := map[string]any{
		"user": map[string]any{"uid": "open-xiaoai"},
		"audio": map[string]any{
			"format": "pcm", "codec": "raw", "rate": 16000, "bits": 16, "channel": 1,
		},
		"request": map[string]any{
			"model_name":      "bigmodel",
			"enable_itn":      true,
			"enable_punc":     true,
			"show_utterances": true,
			"result_type":     "single",
		},
	}
	raw, _ := json.Marshal(init)
	frame := saucHeader(saucMsgFullClient, 0, 0b0001)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(raw)))
	frame = append(frame, raw...)
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		return "", fmt.Errorf("发送初始化失败: %w", err)
	}

	// 2) 音频分片（不带 sequence）
	const chunkSize = 3200
	for off := 0; off < len(pcm); off += chunkSize {
		end := off + chunkSize
		if end > len(pcm) {
			end = len(pcm)
		}
		f := saucHeader(saucMsgAudioOnly, 0, 0)
		f = binary.BigEndian.AppendUint32(f, uint32(end-off))
		f = append(f, pcm[off:end]...)
		if err := conn.Write(ctx, websocket.MessageBinary, f); err != nil {
			return "", fmt.Errorf("发送音频失败: %w", err)
		}
	}

	// 3) 结束帧
	fin := saucHeader(saucMsgAudioOnly, 0b0010, 0)
	fin = binary.BigEndian.AppendUint32(fin, 0)
	if err := conn.Write(ctx, websocket.MessageBinary, fin); err != nil {
		return "", fmt.Errorf("发送结束帧失败: %w", err)
	}

	// 4) 收结果
	last := ""
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithDeadline(ctx, deadline)
		_, msg, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			if last != "" {
				return last, nil
			}
			return "", fmt.Errorf("读取流式结果失败: %w", err)
		}
		text, final, err := parseSaucResponse(msg)
		if err != nil {
			return last, err
		}
		if text != "" {
			last = text
			if onPartial != nil {
				onPartial(text)
			}
		}
		if final {
			log.Printf("⏱️ 流式 ASR: %dms %q", time.Since(start).Milliseconds(), last)
			return last, nil
		}
	}
	return last, nil
}

func parseSaucResponse(msg []byte) (text string, final bool, err error) {
	if len(msg) < 12 {
		return "", false, fmt.Errorf("流式响应过短 (%d 字节)", len(msg))
	}
	msgType := msg[1] >> 4
	flags := msg[1] & 0x0f
	seq := int32(binary.BigEndian.Uint32(msg[4:8]))
	size := binary.BigEndian.Uint32(msg[8:12])
	if uint32(len(msg)) < 12+size {
		return "", false, fmt.Errorf("流式响应长度不符")
	}
	body := msg[12 : 12+size]

	if msgType == saucMsgError {
		return "", true, fmt.Errorf("豆包流式 ASR 错误: %s", strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Result struct {
			Text string `json:"text"`
		} `json:"result"`
	}
	if e := json.Unmarshal(body, &parsed); e != nil {
		return "", false, nil // 非 JSON 帧直接忽略
	}
	return parsed.Result.Text, flags == saucFlagFinal || seq < 0, nil
}
