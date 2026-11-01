package main

// 管理端：语音识别引擎切换。
//
//	GET  /admin/api/asr   当前 provider 与可用列表
//	POST /admin/api/asr   {"provider":"local_alt"} 切换对话识别
//	                     {"wake_provider":"local"} 切换唤醒识别

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// handleWakeTest 在线测试一句话能否命中唤醒词：
//
//	GET /admin/api/wake/test?text=豆包豆包
func (a *adminServer) handleWakeTest(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	cfg := a.getConfig()
	if cfg == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "配置未加载")
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"text":   text,
		"match":  cfg.IsAIWakeWord(text) || a.wakeMatch(text),
		"pinyin": toSyllables(text),
		"rest":   stripWakePrefix(text, cfg, a.wakeWords()),
		"ack":    len([]rune(stripWakePrefix(text, cfg, a.wakeWords()))) <= 3,
	})
}

// wakeWords / wakeMatch 是给测试接口用的小包装。
func (a *adminServer) wakeWords() []string {
	if a.getWakeStore == nil {
		return nil
	}
	if s := a.getWakeStore(); s != nil {
		return s.List()
	}
	return nil
}

func (a *adminServer) wakeMatch(text string) bool {
	if a.getWakeStore == nil {
		return false
	}
	if s := a.getWakeStore(); s != nil {
		return s.Contains(text)
	}
	return false
}

// patchASRProviders 把热切换的引擎写回 config.yaml（保留注释与其它字段）。
func (a *adminServer) patchASRProviders(wake, dialog string) error {
	if a.configPath == "" {
		return fmt.Errorf("配置路径未知")
	}
	raw, err := os.ReadFile(a.configPath)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	set := func(key, val string) {
		if val == "" || len(doc.Content) == 0 {
			return
		}
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			return
		}
		asr := mappingValue(root, "asr")
		if asr == nil || asr.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(asr.Content); i += 2 {
			if asr.Content[i].Value == key {
				asr.Content[i+1].Value = val
				return
			}
		}
		asr.Content = append(asr.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Value: val},
		)
	}
	set("wake_provider", wake)
	set("dialog_provider", dialog)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return writeFileAtomic(a.configPath, out)
}

// mappingValue 在 mapping 节点里按键取值。
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func (a *adminServer) handleASR(w http.ResponseWriter, r *http.Request) {
	router := a.getASRRouter()
	if router == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "服务端唤醒未启用（config.yaml 里 asr.enabled: true 并配置 api_key）",
		})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Provider     string `json:"provider"`
			WakeProvider string `json:"wake_provider"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
			return
		}
		wake, dialog := strings.TrimSpace(req.WakeProvider), strings.TrimSpace(req.Provider)
		if wake != "" {
			if err := router.SetWakeProvider(wake); err != nil {
				writeAdminError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if dialog != "" {
			if err := router.SetDialogProvider(dialog); err != nil {
				writeAdminError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		// 写回 config.yaml：否则重启后又回到文件里的旧值，
		// 管理台两处（识别引擎卡片 / 配置页）就会显示不一致。
		if wake != "" || dialog != "" {
			if err := a.patchASRProviders(wake, dialog); err != nil {
				log.Printf("⚠️ 识别引擎已切换，但写回配置失败: %v", err)
			}
		}
	}

	out := router.Status()
	out["enabled"] = true
	writeAdminJSON(w, http.StatusOK, out)
}
