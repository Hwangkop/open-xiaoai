package main

// 豆包语音识别（火山引擎 v3 大模型录音文件识别）。
//
// 两段式 HTTP：
//   POST /api/v3/auc/bigmodel/submit  提交（音频走 base64 内联，不需要 URL）
//   POST /api/v3/auc/bigmodel/query   轮询结果
//
// 鉴权：x-api-key + X-Api-Resource-Id；两次请求必须用同一个 X-Api-Request-Id。
// 实测 format 必须写 "pcm"（写 "raw" 会被当成无音频，结果为空）。
//
// 返回码：20000000 成功；20000001 处理中；其它为错误。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	doubaoASRSubmitURL = "https://openspeech.bytedance.com/api/v3/auc/bigmodel/submit"
	doubaoASRQueryURL  = "https://openspeech.bytedance.com/api/v3/auc/bigmodel/query"
	doubaoASRResource  = "volc.seedasr.auc"

	asrStatusOK       = "20000000"
	asrStatusPending  = "20000001"
	asrStatusNoSpeech = "20000003" // 无明显人声（静音/纯噪声），不算错误
)

// DoubaoASR 是火山 v3 大模型语音识别客户端。
type DoubaoASR struct {
	apiKey     string
	resourceID string
	client     *http.Client
}

func NewDoubaoASR(apiKey, resourceID string) *DoubaoASR {
	if resourceID == "" {
		resourceID = doubaoASRResource
	}
	return &DoubaoASR{
		apiKey:     strings.TrimSpace(apiKey),
		resourceID: resourceID,
		client:     &http.Client{Timeout: 40 * time.Second},
	}
}

func (a *DoubaoASR) Enabled() bool { return a.apiKey != "" }

type asrResult struct {
	AudioInfo struct {
		Duration int `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Text string `json:"text"`
	} `json:"result"`
}

// Transcribe 识别一段 16k/mono/S16_LE 的 PCM。
func (a *DoubaoASR) Transcribe(ctx context.Context, pcm []byte) (string, error) {
	if !a.Enabled() {
		return "", fmt.Errorf("未配置豆包 ASR 的 api_key")
	}
	if len(pcm) == 0 {
		return "", nil
	}

	start := time.Now()
	reqID := newUUID()
	if err := a.submit(ctx, reqID, pcm); err != nil {
		return "", err
	}
	submitMs := time.Since(start).Milliseconds()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
		text, done, err := a.query(ctx, reqID)
		if err != nil {
			// 任务尚未就绪时偶发错误码，继续轮询
			if strings.Contains(err.Error(), "处理中") {
				continue
			}
			return "", err
		}
		if done {
			log.Printf("⏱️ ASR 耗时: 总 %dms（上传 %dms）", time.Since(start).Milliseconds(), submitMs)
			return text, nil
		}
	}
	return "", fmt.Errorf("豆包 ASR 超时")
}

func (a *DoubaoASR) submit(ctx context.Context, reqID string, pcm []byte) error {
	body := map[string]any{
		"user": map[string]any{"uid": "open-xiaoai"},
		"audio": map[string]any{
			"data":    base64.StdEncoding.EncodeToString(pcm),
			"format":  "pcm",
			"codec":   "raw",
			"rate":    16000,
			"bits":    16,
			"channel": 1,
		},
		"request": map[string]any{
			"model_name":             "bigmodel",
			"enable_itn":             true,
			"enable_punc":            true,
			"enable_ddc":             false,
			"enable_speaker_info":    false,
			"enable_channel_split":   false,
			"show_utterances":        false,
			"vad_segment":            false,
			"sensitive_words_filter": "",
		},
	}
	raw, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doubaoASRSubmitURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	a.setHeaders(req, reqID)
	req.Header.Set("X-Api-Sequence", "-1")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("提交 ASR 任务失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if code := resp.Header.Get("X-Api-Status-Code"); code != asrStatusOK {
		return fmt.Errorf("提交 ASR 任务失败: code=%s msg=%s", code, resp.Header.Get("X-Api-Message"))
	}
	return nil
}

func (a *DoubaoASR) query(ctx context.Context, reqID string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doubaoASRQueryURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", false, err
	}
	a.setHeaders(req, reqID)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("查询 ASR 结果失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	code := resp.Header.Get("X-Api-Status-Code")
	msg := resp.Header.Get("X-Api-Message")
	switch code {
	case asrStatusOK:
		var r asrResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", false, fmt.Errorf("解析 ASR 结果失败: %w", err)
		}
		text := strings.TrimSpace(r.Result.Text)
		if text != "" {
			log.Printf("🗣️ 豆包 ASR: %q (%dms)", text, r.AudioInfo.Duration)
		}
		return text, true, nil
	case asrStatusPending:
		return "", false, fmt.Errorf("处理中")
	case asrStatusNoSpeech:
		// 静音/噪声片段：正常结果（空文本），不要触发回落
		return "", true, nil
	default:
		return "", false, fmt.Errorf("豆包 ASR 错误: code=%s msg=%s", code, msg)
	}
}

func (a *DoubaoASR) setHeaders(req *http.Request, reqID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("X-Api-Resource-Id", a.resourceID)
	req.Header.Set("X-Api-Request-Id", reqID)
}
