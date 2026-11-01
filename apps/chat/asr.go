package main

// 电脑端 ASR：把音箱推上来的语音片段送进 sherpa-onnx 识别成文字。
//
// 用常驻的 Python 子进程（模型只加载一次），通过 stdin/stdout 传 WAV 路径和结果，
// 避免每句话都重新加载模型。
//
// 为什么用 Python 而不是 Go：sherpa-onnx 的 Go 绑定要 cgo，而我们最终要出 Windows
// 单 exe；Python 侧边进程只在开发/自建服务时用，不影响音箱端。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

type ASRWorker struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	stdout *bufio.Scanner
	ready  bool

	python      string
	script      string
	modelDir    string
	altModelDir string // 备用模型（Paraformer），唤醒词二次识别用
	altReady    bool   // 备用模型是否真的加载成功（以 worker 的 ready 行为准）
}

type asrRequest struct {
	WAV string `json:"wav"`
	Alt bool   `json:"alt,omitempty"`
}

type asrResponse struct {
	Text  string `json:"text"`
	Error string `json:"error"`
}

// NewASRWorker 创建一个 ASR 工作进程（不会立即启动）。
func NewASRWorker(python, script, modelDir, altModelDir string) *ASRWorker {
	if python == "" {
		python = "python3"
	}
	return &ASRWorker{python: python, script: script, modelDir: modelDir, altModelDir: altModelDir}
}

// Start 启动子进程（加载模型约需 1~3 秒）。
func (a *ASRWorker) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.startLocked()
}

func (a *ASRWorker) startLocked() error {
	if a.ready {
		return nil
	}
	if a.script == "" {
		return fmt.Errorf("未配置 ASR 脚本路径")
	}
	cmd := exec.Command(a.python, a.script)
	if a.modelDir != "" {
		cmd.Env = append(os.Environ(), "ASR_MODEL_DIR="+a.modelDir)
	}
	if a.altModelDir != "" {
		cmd.Env = append(cmd.Env, "ASR_ALT_MODEL_DIR="+a.altModelDir)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ASR 进程失败: %w", err)
	}

	a.cmd = cmd
	a.stdin = bufio.NewWriter(stdin)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	a.stdout = sc
	// 第一行是 worker 的 {"ready":true,"engine":...,"alt":bool}
	if !sc.Scan() {
		_ = cmd.Process.Kill()
		return fmt.Errorf("ASR 进程启动后无响应")
	}
	a.ready = true
	// 以 worker 实际加载结果为准：配了备选模型目录但加载失败时，不要谎报能兜底。
	a.altReady = a.altModelDir != ""
	var ready struct {
		Ready bool `json:"ready"`
		Alt   bool `json:"alt"`
	}
	if json.Unmarshal(sc.Bytes(), &ready) == nil && ready.Ready {
		a.altReady = ready.Alt
	}
	log.Printf("🧠 ASR 进程已启动: %s %s（备用模型: %v）", a.python, a.script, a.altReady)
	return nil
}

// HasAlt 备用模型（Paraformer）是否可用。
func (a *ASRWorker) HasAlt() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ready && a.altReady
}

// Transcribe 识别一个 WAV 文件，返回文本。
func (a *ASRWorker) Transcribe(wavPath string) (string, error) {
	return a.transcribeWith(wavPath, false)
}

// TranscribeAlt 用备用模型（Paraformer）识别同一段音频。
func (a *ASRWorker) TranscribeAlt(wavPath string) (string, error) {
	return a.transcribeWith(wavPath, true)
}

func (a *ASRWorker) transcribeWith(wavPath string, alt bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.ready {
		if err := a.startLocked(); err != nil {
			return "", err
		}
	}

	req, _ := json.Marshal(asrRequest{WAV: wavPath, Alt: alt})
	if _, err := a.stdin.Write(append(req, '\n')); err != nil {
		a.ready = false
		return "", fmt.Errorf("写入 ASR 请求失败: %w", err)
	}
	if err := a.stdin.Flush(); err != nil {
		a.ready = false
		return "", err
	}

	if !a.stdout.Scan() {
		a.ready = false
		if err := a.stdout.Err(); err != nil {
			return "", fmt.Errorf("ASR 进程无响应: %w", err)
		}
		return "", fmt.Errorf("ASR 进程已退出")
	}
	var resp asrResponse
	if err := json.Unmarshal(a.stdout.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("解析 ASR 响应失败: %w", err)
	}
	if resp.Error != "" {
		return "", fmt.Errorf("ASR 失败: %s", resp.Error)
	}
	return resp.Text, nil
}

// Close 结束子进程。
func (a *ASRWorker) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd != nil && a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
	}
	a.ready = false
}

var _ = time.Now
