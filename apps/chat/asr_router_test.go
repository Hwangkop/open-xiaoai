package main

import (
	"context"
	"testing"
)

func TestASRConfigProviderNames(t *testing.T) {
	cases := []struct {
		name   string
		cfg    ASRConfig
		wake   string
		dialog string
	}{
		{
			name:   "空配置默认本地",
			cfg:    ASRConfig{},
			wake:   asrProviderLocal,
			dialog: asrProviderLocal,
		},
		{
			name:   "显式配置优先",
			cfg:    ASRConfig{WakeProvider: "doubao", DialogProvider: "doubao_stream"},
			wake:   asrProviderDoubao,
			dialog: asrProviderDoubaoStream,
		},
		{
			name:   "旧字段 local + 备选模型升级成 local_alt",
			cfg:    ASRConfig{Provider: "local", AltModelDir: "E:/models/paraformer"},
			wake:   asrProviderLocal,
			dialog: asrProviderLocalAlt,
		},
		{
			name:   "旧字段 doubao 只影响对话",
			cfg:    ASRConfig{Provider: "doubao"},
			wake:   asrProviderLocal,
			dialog: asrProviderDoubao,
		},
		{
			name:   "新字段不被旧字段覆盖",
			cfg:    ASRConfig{Provider: "doubao", DialogProvider: "local"},
			wake:   asrProviderLocal,
			dialog: asrProviderLocal,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.WakeProviderName(); got != c.wake {
				t.Errorf("WakeProviderName() = %q, want %q", got, c.wake)
			}
			if got := c.cfg.DialogProviderName(); got != c.dialog {
				t.Errorf("DialogProviderName() = %q, want %q", got, c.dialog)
			}
		})
	}
}

func TestRouterDefaults(t *testing.T) {
	// 没有本地模型也没有云端 key：仍然落在 local，调用时会报错而不是 panic
	r := newASRRouter("", "", nil, nil, nil)
	if r.WakeProvider() != asrProviderLocal {
		t.Errorf("wake = %q, want local", r.WakeProvider())
	}
	if r.DialogProvider() != asrProviderLocal {
		t.Errorf("dialog = %q, want local", r.DialogProvider())
	}
}

func TestRouterDialogLocalReusesText(t *testing.T) {
	r := &asrRouter{wake: asrProviderLocal, dialog: asrProviderLocal}
	got, err := r.TranscribeDialog(context.Background(), "打开客厅的灯", nil, "")
	if err != nil {
		t.Fatalf("TranscribeDialog() error = %v", err)
	}
	if got != "打开客厅的灯" {
		t.Errorf("TranscribeDialog() = %q, want 原文本", got)
	}
}

func TestSetDialogProviderValidation(t *testing.T) {
	r := &asrRouter{wake: asrProviderLocal, dialog: asrProviderLocal}
	if err := r.SetDialogProvider("nonsense"); err == nil {
		t.Error("未知引擎应当报错")
	}
	// 云端没配 key，不能切过去
	if err := r.SetDialogProvider(asrProviderDoubao); err == nil {
		t.Error("未配置的云端引擎应当报错")
	}
	if r.DialogProvider() != asrProviderLocal {
		t.Errorf("切换失败后 provider 被改成了 %q", r.DialogProvider())
	}
}

func TestProviderLabel(t *testing.T) {
	if got := providerLabel(asrProviderDoubaoStream); got != "火山引擎流式" {
		t.Errorf("providerLabel(doubao_stream) = %q", got)
	}
	if got := providerLabel("unknown"); got != "unknown" {
		t.Errorf("providerLabel(unknown) = %q", got)
	}
}
