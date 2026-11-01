package main

// ASR 路由：唤醒监听和对话识别分开配置。
//
//   - 唤醒监听：默认本地 SenseVoice（快），没听准时由引擎再让 Paraformer 认一次
//     （TranscribeAlt），所以唤醒侧就是「SenseVoice + Paraformer 兜底」。
//   - 对话识别：唤醒之后那句话用哪套引擎，可选
//     doubao_stream（火山流式）/ doubao（火山整段）/ local（SenseVoice）/
//     local_alt（SenseVoice + Paraformer 兜底）。
//
// 切换入口：GET/POST /admin/api/asr
//
//	{"provider":"local_alt"}        切换对话识别
//	{"wake_provider":"local"}       切换唤醒识别
//
// 本地引擎不可用时会自动回落到另一种，避免某一边抖动时整个语音链路瘫掉。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	asrProviderDoubao       = "doubao"        // 火山引擎整段提交（准确，1.3s+）
	asrProviderDoubaoStream = "doubao_stream" // 火山引擎流式（最终结果 ~0.2s）
	asrProviderLocal        = "local"         // 本地 SenseVoice
	asrProviderLocalAlt     = "local_alt"     // 本地 SenseVoice + Paraformer 兜底
)

type asrRouter struct {
	mu      sync.RWMutex
	wake    string // 唤醒监听用哪套
	dialog  string // 唤醒之后的对话识别用哪套
	doubao  *DoubaoASR
	stream  *DoubaoStreamASR
	local   *ASRWorker
	lastErr string
}

func newASRRouter(wake, dialog string, doubao *DoubaoASR, stream *DoubaoStreamASR, local *ASRWorker) *asrRouter {
	r := &asrRouter{doubao: doubao, stream: stream, local: local}

	r.wake = normalizeASRProvider(wake)
	if r.wake == "" || !r.canSwitch(r.wake) {
		r.wake = asrProviderLocal
	}

	r.dialog = normalizeASRProvider(dialog)
	if r.dialog == "" || !r.canSwitch(r.dialog) {
		r.dialog = r.defaultDialog()
	}
	return r
}

// defaultDialog 没配置对话引擎时按可用性挑一个。
func (r *asrRouter) defaultDialog() string {
	if r.local != nil {
		if r.local.HasAlt() {
			return asrProviderLocalAlt
		}
		return asrProviderLocal
	}
	if r.stream != nil && r.stream.Enabled() {
		return asrProviderDoubaoStream
	}
	if r.doubao != nil && r.doubao.Enabled() {
		return asrProviderDoubao
	}
	return asrProviderLocal
}

func normalizeASRProvider(p string) string {
	switch p = strings.ToLower(strings.TrimSpace(p)); p {
	case asrProviderDoubao, asrProviderDoubaoStream, asrProviderLocal, asrProviderLocalAlt:
		return p
	}
	return ""
}

func providerLabel(p string) string {
	switch p {
	case asrProviderDoubaoStream:
		return "火山引擎流式"
	case asrProviderDoubao:
		return "火山引擎整段"
	case asrProviderLocal:
		return "SenseVoice"
	case asrProviderLocalAlt:
		return "SenseVoice+Paraformer 兜底"
	}
	return p
}

// canSwitch 判断当前进程里这个引擎能不能用。
func (r *asrRouter) canSwitch(p string) bool {
	switch p {
	case asrProviderDoubao:
		return r.doubao != nil && r.doubao.Enabled()
	case asrProviderDoubaoStream:
		return r.stream != nil && r.stream.Enabled()
	case asrProviderLocal:
		return r.local != nil
	case asrProviderLocalAlt:
		// 兜底模型没真正加载成功时，local_alt 等价于 local，直接判为不可用
		return r.local != nil && r.local.HasAlt()
	}
	return false
}

func (r *asrRouter) Provider() string { return r.DialogProvider() }

func (r *asrRouter) DialogProvider() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dialog
}

func (r *asrRouter) WakeProvider() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.wake
}

// SetProvider 兼容旧接口：等同于切换对话识别。
func (r *asrRouter) SetProvider(p string) error { return r.SetDialogProvider(p) }

// SetDialogProvider 切换唤醒之后的对话识别引擎。
func (r *asrRouter) SetDialogProvider(p string) error {
	p = normalizeASRProvider(p)
	if p == "" {
		return fmt.Errorf("未知的对话识别引擎 %q（可选 doubao_stream / doubao / local / local_alt）", p)
	}
	if !r.canSwitch(p) {
		return fmt.Errorf("%s 当前不可用", providerLabel(p))
	}
	r.mu.Lock()
	changed := r.dialog != p
	r.dialog = p
	r.mu.Unlock()
	if changed {
		log.Printf("🔄 对话识别切换到 %s", providerLabel(p))
	}
	return nil
}

// SetWakeProvider 切换唤醒监听引擎（默认 local，一般不用改）。
func (r *asrRouter) SetWakeProvider(p string) error {
	p = normalizeASRProvider(p)
	if p == "" {
		return fmt.Errorf("未知的唤醒识别引擎 %q（可选 local / local_alt / doubao / doubao_stream）", p)
	}
	if !r.canSwitch(p) {
		return fmt.Errorf("%s 当前不可用", providerLabel(p))
	}
	r.mu.Lock()
	changed := r.wake != p
	r.wake = p
	r.mu.Unlock()
	if changed {
		log.Printf("🔄 唤醒识别切换到 %s", providerLabel(p))
	}
	return nil
}

// Status 给管理接口用。
func (r *asrRouter) Status() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	available := []string{}
	if r.local != nil {
		available = append(available, asrProviderLocal)
		if r.local.HasAlt() {
			available = append(available, asrProviderLocalAlt)
		}
	}
	if r.stream != nil && r.stream.Enabled() {
		available = append(available, asrProviderDoubaoStream)
	}
	if r.doubao != nil && r.doubao.Enabled() {
		available = append(available, asrProviderDoubao)
	}
	out := map[string]any{
		// provider 保留旧字段名，含义是对话识别
		"provider":        r.dialog,
		"dialog_provider": r.dialog,
		"wake_provider":   r.wake,
		"available":       available,
		"labels": map[string]string{
			asrProviderLocalAlt:     providerLabel(asrProviderLocalAlt),
			asrProviderLocal:        providerLabel(asrProviderLocal),
			asrProviderDoubaoStream: providerLabel(asrProviderDoubaoStream),
			asrProviderDoubao:       providerLabel(asrProviderDoubao),
		},
	}
	if r.lastErr != "" {
		out["last_error"] = r.lastErr
	}
	return out
}

// TranscribeWake 唤醒监听：默认本地 SenseVoice（快），本地不可用才走云端。
func (r *asrRouter) TranscribeWake(ctx context.Context, pcm []byte, wav string) (string, error) {
	start := time.Now()
	r.mu.RLock()
	wake := r.wake
	r.mu.RUnlock()

	if wake == asrProviderDoubao || wake == asrProviderDoubaoStream {
		text, err := r.transcribeWith(ctx, wake, pcm, wav)
		if err == nil {
			logRecognition("唤醒", "wake_provider="+wake+" engine="+wake, time.Since(start), text)
		}
		return text, err
	}

	r.mu.RLock()
	local := r.local
	r.mu.RUnlock()
	if local != nil && wav != "" {
		text, err := local.Transcribe(wav)
		if err == nil {
			if strings.TrimSpace(text) != "" {
				logRecognition("唤醒", "wake_provider=local engine=sensevoice", time.Since(start), text)
				return text, nil
			}
			// SenseVoice 什么都没听出来：让 Paraformer 兜一次（只用于唤醒判定）
			altText, altErr := r.TranscribeAlt(ctx, wav)
			if altErr == nil && strings.TrimSpace(altText) != "" {
				logRecognition("唤醒", "wake_provider=local engine=paraformer(兜底)", time.Since(start), altText)
				return altText, nil
			}
			logRecognition("唤醒", "wake_provider=local engine=sensevoice(空)", time.Since(start), text)
			return text, nil
		}
		log.Printf("⚠️ 唤醒识别失败（%v），改用云端引擎", err)
	}
	text, engine, err := r.transcribeWakeFallback(ctx, pcm, wav)
	if err == nil {
		logRecognition("唤醒", "engine="+engine, time.Since(start), text)
	}
	return text, err
}

// transcribeWakeFallback 本地模型不可用时的兜底顺序：流式 → 整段。
func (r *asrRouter) transcribeWakeFallback(ctx context.Context, pcm []byte, wav string) (string, string, error) {
	if r.stream != nil && r.stream.Enabled() {
		text, err := r.stream.TranscribeFinalOnly(ctx, pcm)
		return text, asrProviderDoubaoStream, err
	}
	if r.doubao != nil && r.doubao.Enabled() {
		text, err := r.doubao.Transcribe(ctx, pcm)
		return text, asrProviderDoubao, err
	}
	return "", "", fmt.Errorf("没有可用的 ASR 引擎")
}

// TranscribeDialog 用对话识别引擎认一遍。
//
// current 是唤醒引擎已经给出的文本（SenseVoice 的结果）：对话引擎也是本地模型时
// 直接复用它，不白跑一次；只有选了云端引擎、或本地兜底需要 Paraformer 时才重新识别。
// 云端失败返回错误，调用方继续用 current，链路不会因为一次 API 抖动而断掉。
func (r *asrRouter) TranscribeDialog(ctx context.Context, current string, pcm []byte, wav string) (string, error) {
	start := time.Now()
	r.mu.RLock()
	dialog := r.dialog
	r.mu.RUnlock()

	switch dialog {
	case asrProviderLocal:
		logRecognition("对话", "dialog_provider=local engine=sensevoice(复用唤醒结果)", time.Since(start), current)
		return current, nil
	case asrProviderLocalAlt:
		// SenseVoice 听出来了就够用；只有空/太短（噪声碎片、半句话）才请 Paraformer
		if len([]rune(stripPunct(current))) > 2 {
			logRecognition("对话", "dialog_provider=local_alt engine=sensevoice(复用唤醒结果)", time.Since(start), current)
			return current, nil
		}
		altText, err := r.TranscribeAlt(ctx, wav)
		if err != nil || strings.TrimSpace(altText) == "" ||
			len([]rune(stripPunct(altText))) <= len([]rune(stripPunct(current))) {
			logRecognition("对话", "dialog_provider=local_alt engine=sensevoice(兜底没变好)", time.Since(start), current)
			return current, nil
		}
		logRecognition("对话", "dialog_provider=local_alt engine=paraformer(兜底)", time.Since(start), altText)
		return altText, nil
	default:
		text, err := r.transcribeWith(ctx, dialog, pcm, wav)
		if err != nil {
			logRecognition("对话(失败)", "dialog_provider="+dialog+" engine="+dialog, time.Since(start), "")
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			logRecognition("对话", "dialog_provider="+dialog+" engine="+dialog+"(空)", time.Since(start), text)
			return "", fmt.Errorf("%s 没识别出内容", providerLabel(dialog))
		}
		logRecognition("对话", "dialog_provider="+dialog+" engine="+dialog, time.Since(start), text)
		return text, nil
	}
}

// logRecognition 统一的识别日志：引擎 / 耗时 / 字数 / 原文。
// 格式固定（provider、ms、chars 都是 ASCII 键），方便后面 grep/awk 对比。
func logRecognition(stage, fields string, elapsed time.Duration, text string) {
	text = strings.TrimSpace(text)
	log.Printf("⏱️ [识别] %s %s %dms chars=%d text=%q",
		stage, fields, elapsed.Milliseconds(), len([]rune(text)), text)
}

// TranscribeAlt 用本地备用模型（Paraformer）再识别一次，唤醒词兜底用。
func (r *asrRouter) TranscribeAlt(ctx context.Context, wav string) (string, error) {
	r.mu.RLock()
	local := r.local
	r.mu.RUnlock()
	if local == nil || strings.TrimSpace(wav) == "" {
		return "", fmt.Errorf("本地备用模型不可用")
	}
	return local.TranscribeAlt(wav)
}

func (r *asrRouter) transcribeWith(ctx context.Context, provider string, pcm []byte, wav string) (string, error) {
	switch provider {
	case asrProviderDoubao:
		if r.doubao == nil || !r.doubao.Enabled() {
			return "", fmt.Errorf("豆包 ASR 未配置")
		}
		return r.doubao.Transcribe(ctx, pcm)
	case asrProviderDoubaoStream:
		if r.stream == nil || !r.stream.Enabled() {
			return "", fmt.Errorf("豆包流式 ASR 未配置")
		}
		return r.stream.TranscribeFinalOnly(ctx, pcm)
	case asrProviderLocal, asrProviderLocalAlt:
		if r.local == nil {
			return "", fmt.Errorf("本地 ASR 未就绪")
		}
		if wav == "" {
			return "", fmt.Errorf("本地 ASR 需要音频文件路径")
		}
		return r.local.Transcribe(wav)
	}
	return "", fmt.Errorf("未知 provider %q", provider)
}
