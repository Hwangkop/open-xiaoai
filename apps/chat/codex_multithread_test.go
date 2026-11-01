package main

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// 手动跑：CODEX_MULTI_TEST=1 go test -run TestCodexMultiThread -v .
// 验证同一个 app-server 里两条 thread 并发、且输出不会串。
func TestCodexMultiThread(t *testing.T) {
	if os.Getenv("CODEX_MULTI_TEST") == "" {
		t.Skip("设置 CODEX_MULTI_TEST=1 才跑")
	}
	srv := NewCodexAppServer(CodexConfig{
		Bin:              "codex",
		CodexHome:        os.Getenv("HOME") + "/.codex-deepseek",
		Workspace:        "/tmp",
		Timeout:          90,
		BaseInstructions: "回答要短，一句话。",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	defer srv.Close()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	runner := NewCodexThreadRunner(srv)

	var wg sync.WaitGroup
	var a, b string
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		a, errA = srv.Turn(ctx, "只回答两个字：苹果", nil)
	}()
	go func() {
		defer wg.Done()
		b, errB = runner.Ask(ctx, "只回答两个字：香蕉")
	}()
	wg.Wait()

	if errA != nil || errB != nil {
		t.Fatalf("对话 err=%v / 任务 err=%v", errA, errB)
	}
	t.Logf("对话=%q 任务=%q", a, b)
	if !strings.Contains(a, "苹果") {
		t.Fatalf("对话串味: %q", a)
	}
	if !strings.Contains(b, "香蕉") {
		t.Fatalf("任务串味: %q", b)
	}
}
