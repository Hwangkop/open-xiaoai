package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 手动跑：CODEX_MCP_TEST=1 go test -run TestCodexAppServerMCP -v .
func TestCodexAppServerMCP(t *testing.T) {
	if os.Getenv("CODEX_MCP_TEST") == "" {
		t.Skip("设置 CODEX_MCP_TEST=1 才跑")
	}
	home := os.Getenv("HOME")
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = home + "/.codex-deepseek"
	}
	srv := NewCodexAppServer(CodexConfig{
		Bin:              "codex",
		CodexHome:        codexHome,
		Workspace:        "/tmp",
		Timeout:          90,
		BaseInstructions: "你是语音助手。需要信息时优先使用 MCP 工具。回答简洁。",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	defer srv.Close()

	prompt := os.Getenv("CODEX_MCP_PROMPT")
	if prompt == "" {
		prompt = "调用 MCP 工具 openxiaoai 的 get_time，把工具返回的原文照抄出来，不要用 shell。"
	}
	start := time.Now()
	first := time.Duration(0)
	var deltas []string
	out, err := srv.Turn(ctx, prompt, func(s string) {
		if first == 0 {
			first = time.Since(start)
		}
		deltas = append(deltas, s)
	})
	if err != nil {
		t.Fatalf("Turn 失败: %v", err)
	}
	t.Logf("首字 %v，总 %v", first, time.Since(start))
	t.Logf("输出: %s", out)
	if strings.Contains(os.Getenv("CODEX_MCP_PROMPT"), "晴天") {
		if !strings.Contains(out, "播放") && !strings.Contains(out, "歌") {
			t.Fatalf("没走音乐工具: %q", out)
		}
		return
	}
	if !strings.Contains(out, "2026-") && !strings.Contains(out, "2025-") {
		t.Fatalf("没有拿到工具返回的时间: %q", out)
	}
}
