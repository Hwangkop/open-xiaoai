package main

// 把 Codex CLI 包成一个对话后端。
//
// 调用方式：
//	codex -p <profile> exec --json --skip-git-repo-check --ephemeral -C <workspace> "<prompt>"
//
// stdout 是 JSONL 事件流，我们只取 type=item.completed 且 item.type=agent_message
// 的最后一条文本（codex 目前不提供逐 token 增量）。
//
// profile 指向 ~/.codex/<profile>.config.toml，里面配好 DeepSeek 的 provider。
// workspace 里的 AGENTS.md 就是"系统提示词"（codex 会把它注入给模型）。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

type CodexConfig struct {
	Bin       string `yaml:"bin"`
	Profile   string `yaml:"profile"`
	Workspace string `yaml:"workspace"`
	Timeout   int    `yaml:"timeout_seconds"`
	// CodexHome 指向独立的 CODEX_HOME（内含 config.toml 配好 DeepSeek provider）
	CodexHome string `yaml:"codex_home"`
	// BaseInstructions 作为 codex 线程的基础指令（相当于系统提示词）
	BaseInstructions string `yaml:"base_instructions"`
}

type CodexClient struct {
	cfg CodexConfig
}

func NewCodexClient(cfg CodexConfig) *CodexClient {
	if strings.TrimSpace(cfg.Bin) == "" {
		cfg.Bin = "codex"
	}
	// 用独立的 CODEX_HOME 时不需要 -p（profile 在那种模式下会被拒绝）
	if strings.TrimSpace(cfg.Profile) == "" && strings.TrimSpace(cfg.CodexHome) == "" {
		cfg.Profile = "deepseek"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90
	}
	return &CodexClient{cfg: cfg}
}

type codexEvent struct {
	Type string `json:"type"`
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
	} `json:"item"`
}

// Ask 跑一轮 codex，返回助手最终回复的文本。
func (c *CodexClient) Ask(ctx context.Context, prompt string) (string, error) {
	if _, err := exec.LookPath(c.cfg.Bin); err != nil {
		return "", fmt.Errorf("找不到 codex 可执行文件: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.Timeout)*time.Second)
	defer cancel()

	args := []string{"exec"}
	if c.cfg.Profile != "" {
		args = []string{"-p", c.cfg.Profile, "exec"}
	}
	args = append(args,
		"--json",
		"--skip-git-repo-check",
		"--ephemeral",
	)
	if c.cfg.Workspace != "" {
		args = append(args, "-C", c.cfg.Workspace)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, c.cfg.Bin, args...)
	if home := strings.TrimSpace(c.cfg.CodexHome); home != "" {
		cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = nil // codex 的 warning 很多，不要污染日志

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 codex 失败: %w", err)
	}

	var last string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type == "item.completed" && ev.Item.Type == "agent_message" && strings.TrimSpace(ev.Item.Text) != "" {
			last = ev.Item.Text
		}
	}
	_ = cmd.Wait()

	if strings.TrimSpace(last) == "" {
		return "", fmt.Errorf("codex 没有返回有效回复（耗时 %dms）", time.Since(start).Milliseconds())
	}
	log.Printf("🧩 codex 回复 %d 字（耗时 %dms）", len([]rune(last)), time.Since(start).Milliseconds())
	return last, nil
}

// ---------------- 适配成 openai 流 ----------------

type codexStream struct {
	ch     chan openai.ChatCompletionStreamResponse
	errs   chan error
	cancel context.CancelFunc
	closed chan struct{}
}

func newCodexStream(parent context.Context, client *CodexClient, prompt string) *codexStream {
	ctx, cancel := context.WithCancel(parent)
	s := &codexStream{
		ch:     make(chan openai.ChatCompletionStreamResponse, 4),
		errs:   make(chan error, 1),
		cancel: cancel,
		closed: make(chan struct{}),
	}
	go func() {
		defer close(s.ch)
		text, err := client.Ask(ctx, prompt)
		if err != nil {
			if ctx.Err() == nil {
				s.errs <- err
			}
			return
		}
		select {
		case s.ch <- openai.ChatCompletionStreamResponse{
			Choices: []openai.ChatCompletionStreamChoice{
				{Delta: openai.ChatCompletionStreamChoiceDelta{Content: text}},
			},
		}:
		case <-ctx.Done():
		}
	}()
	return s
}

func (s *codexStream) Recv() (openai.ChatCompletionStreamResponse, error) {
	select {
	case resp, ok := <-s.ch:
		if !ok {
			select {
			case err := <-s.errs:
				return openai.ChatCompletionStreamResponse{}, err
			default:
			}
			return openai.ChatCompletionStreamResponse{}, io.EOF
		}
		return resp, nil
	case <-s.closed:
		return openai.ChatCompletionStreamResponse{}, io.EOF
	}
}

func (s *codexStream) Close() error {
	select {
	case <-s.closed:
		return nil
	default:
	}
	close(s.closed)
	s.cancel()
	return nil
}
