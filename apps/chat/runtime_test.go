package main

import "testing"

func TestConfigRequiresRestartIgnoresAuthChanges(t *testing.T) {
	oldConfig := defaultConfig()
	newConfig := defaultConfig()
	newConfig.Auth.Users = []AuthUser{{Username: "admin", Password: "secret"}}

	if configRequiresRestart(oldConfig, newConfig) {
		t.Fatal("expected auth changes to apply to new requests without restart")
	}
}

func TestConfigRequiresRestartForLLMChanges(t *testing.T) {
	oldConfig := defaultConfig()
	newConfig := defaultConfig()
	newConfig.LLM.Model = "different-model"

	if !configRequiresRestart(oldConfig, newConfig) {
		t.Fatal("expected LLM client changes to require restart")
	}
}

func TestConfigRequiresRestartForKnowledgeChanges(t *testing.T) {
	oldConfig := defaultConfig()
	newConfig := defaultConfig()
	newConfig.Knowledge.Root = "/different/knowledge"

	if !configRequiresRestart(oldConfig, newConfig) {
		t.Fatal("expected knowledge worker changes to require restart")
	}
}

func TestConfigRequiresRestartForRuntimeComponentChanges(t *testing.T) {
	for name, mutate := range map[string]func(*AppConfig){
		"chat provider":   func(cfg *AppConfig) { cfg.Chat.Provider = "llm" },
		"codex workspace": func(cfg *AppConfig) { cfg.Codex.Workspace = "/different/workspace" },
		"codex home":      func(cfg *AppConfig) { cfg.Codex.CodexHome = "/different/home" },
		"doubao web":      func(cfg *AppConfig) { cfg.DoubaoWeb.Enabled = true },
		"schedules":       func(cfg *AppConfig) { cfg.Schedules.Enabled = true },
		"bilibili":        func(cfg *AppConfig) { cfg.Bilibili.Ffmpeg = "/different/ffmpeg" },
	} {
		t.Run(name, func(t *testing.T) {
			oldConfig := defaultConfig()
			newConfig := defaultConfig()
			mutate(newConfig)
			if !configRequiresRestart(oldConfig, newConfig) {
				t.Fatal("expected config change to require restart")
			}
		})
	}
}

func TestConfigRequiresRestartForASRInfrastructureOnly(t *testing.T) {
	oldConfig := defaultConfig()
	oldConfig.ASR.Enabled = true

	providerOnly := defaultConfig()
	providerOnly.ASR.Enabled = true
	providerOnly.ASR.WakeProvider = "doubao_stream"
	providerOnly.ASR.DialogProvider = "doubao_stream"
	if configRequiresRestart(oldConfig, providerOnly) {
		t.Fatal("识别引擎切换应热生效，不应要求重启")
	}

	credentials := defaultConfig()
	credentials.ASR.Enabled = true
	credentials.ASR.APIKey = "different-key"
	if !configRequiresRestart(oldConfig, credentials) {
		t.Fatal("ASR 凭据变更应要求重启")
	}
}
