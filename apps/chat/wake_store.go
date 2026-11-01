package main

// 唤醒词的可编辑扩展表。
//
// 内置词写在 config.go 里（编译期），这里管理用户自己加的词，
// 落到 wake-words.json，运行时热生效（不用重启）。
//
// 匹配时两者都认：内置词保证基本可用，扩展表让你随时补漏。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type wakeStore struct {
	mu    sync.RWMutex
	path  string
	words []string
}

func newWakeStore(configPath string) *wakeStore {
	dir := filepath.Dir(configPath)
	if dir == "" {
		dir = "."
	}
	s := &wakeStore{path: filepath.Join(dir, "wake-words.json")}
	s.load()
	return s
}

func (s *wakeStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var words []string
	if err := json.Unmarshal(raw, &words); err != nil {
		log.Printf("⚠️ 唤醒词文件解析失败: %v", err)
		return
	}
	s.words = words
}

func (s *wakeStore) save() error {
	raw, err := json.MarshalIndent(s.words, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o644)
}

// List 返回用户扩展的唤醒词。
func (s *wakeStore) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.words))
	copy(out, s.words)
	return out
}

// Add 添加一个词（去重），返回是否新增。
func (s *wakeStore) Add(word string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.words {
		if w == word {
			return false, nil
		}
	}
	s.words = append(s.words, word)
	return true, s.save()
}

// Remove 删除一个词。
func (s *wakeStore) Remove(word string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.words {
		if w == word {
			s.words = append(s.words[:i], s.words[i+1:]...)
			return true, s.save()
		}
	}
	return false, nil
}

// Contains 判断文本里是否包含用户自定义的唤醒词。
func (s *wakeStore) Contains(text string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := stripPunct(text)
	for _, w := range s.words {
		if w != "" && strings.Contains(t, w) {
			return true
		}
	}
	return false
}
