package main

// 指令隔离由当前连接维持短租约；断线不再认领新轮次，已认领轮次继续挡迟到动作。
import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/cxjava/open-xiaoai/apps/client/services/connect"
	"github.com/cxjava/open-xiaoai/apps/client/utils"
)

const instructionGuardDir = "/tmp/xiaoai-native-guard"

var instructionGuard struct {
	sync.Mutex
	enabled  bool
	keywords []string
}

func guardReadyPID() int {
	data, err := os.ReadFile(filepath.Join(instructionGuardDir, "ready"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0
	}
	current, _ := utils.NativeProcessState(nativeAssistantName)
	if current != pid {
		return 0
	}
	maps, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil || !strings.Contains(string(maps), "/data/open-xiaoai/native_guard.so") {
		return 0
	}
	return pid
}

func writeInstructionLeaseLocked() error {
	if guardReadyPID() == 0 {
		return fmt.Errorf("原生指令钩子未加载到当前 aivs")
	}
	body := fmt.Sprintf("1\n%d\n%d\n", os.Getpid(), time.Now().Add(12*time.Second).Unix())
	for _, kw := range instructionGuard.keywords {
		if kw == "" {
			continue
		}
		if strings.ContainsAny(kw, "\r\n\x00") {
			return fmt.Errorf("原生透传关键词不能包含换行或空字符")
		}
		body += kw + "\n"
	}
	if len(body) >= 8192 {
		return fmt.Errorf("原生指令策略超过大小限制")
	}
	temp, err := os.CreateTemp(instructionGuardDir, ".policy-")
	if err != nil {
		return err
	}
	path := temp.Name()
	defer os.Remove(path)
	if _, err = temp.WriteString(body); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(instructionGuardDir, "policy"))
}

func disableInstructionGuard() {
	instructionGuard.Lock()
	defer instructionGuard.Unlock()
	instructionGuard.enabled = false
	_ = os.Remove(filepath.Join(instructionGuardDir, "policy"))
	nativeGuardEnabled.Store(false)
}

func refreshInstructionGuard(conn *websocket.Conn) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !connect.GetMessageManager().IsCurrent(conn) {
			return
		}
		instructionGuard.Lock()
		if instructionGuard.enabled {
			if err := writeInstructionLeaseLocked(); err != nil {
				// 租约失效就停止认领；逐轮确认失败时走已有冻结兼容路径。
				_ = os.Remove(filepath.Join(instructionGuardDir, "policy"))
				log.Printf("⚠️ [native-guard] 刷新指令隔离租约失败：%v", err)
			}
		}
		instructionGuard.Unlock()
	}
}

func instructionGuardClaimed(line string) bool {
	var msg struct {
		Header struct {
			DialogID string `json:"dialog_id"`
		} `json:"header"`
	}
	if json.Unmarshal([]byte(line), &msg) != nil {
		return false
	}
	id := msg.Header.DialogID
	if len(id) == 0 || len(id) >= 96 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	pid := guardReadyPID()
	if pid == 0 {
		return false
	}
	data, err := os.ReadFile(filepath.Join(instructionGuardDir, "claim-"+id))
	return err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(pid)
}
