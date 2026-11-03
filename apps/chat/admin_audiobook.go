package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

func (a *adminServer) handleAudiobooks(w http.ResponseWriter, r *http.Request) {
	b := a.audiobooks
	if b == nil {
		if r.Method == http.MethodGet {
			writeAdminJSON(w, http.StatusOK, map[string]any{"enabled": false, "message": "听书未启用，请启用知识库及 audiobook.enabled 后重启服务"})
			return
		}
		writeAdminError(w, http.StatusServiceUnavailable, "听书未启用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		switch strings.TrimPrefix(r.URL.Path, "/admin/api/audiobooks") {
		case "", "/":
			books, err := b.catalog()
			if err != nil {
				writeAdminError(w, 500, err.Error())
				return
			}
			writeAdminJSON(w, 200, map[string]any{"enabled": true, "books": books, "playback": b.status()})
		case "/status":
			writeAdminJSON(w, 200, b.status())
		default:
			writeAdminError(w, 404, "听书接口不存在")
		}
	case http.MethodPost:
		var req struct {
			Action    string `json:"action"`
			Book      string `json:"book"`
			ChapterID string `json:"chapter_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			writeAdminError(w, 400, "请求格式错误")
			return
		}
		var msg string
		var err error
		if req.Action == "play" {
			msg, err = b.request(req.Book, req.ChapterID)
		} else {
			msg, err = b.control(req.Action)
		}
		if err != nil {
			writeAdminError(w, 400, err.Error())
			return
		}
		writeAdminJSON(w, 202, map[string]any{"message": msg, "playback": b.status()})
	default:
		writeAdminError(w, 405, "请求方法不支持")
	}
}

// 音箱不携带管理台凭证，只开放不可遍历的内容散列文件；禁止目录列表。
var audiobookAudioNameRE = regexp.MustCompile(`^[a-f0-9]{32}\.wav$`)

func (a *adminServer) handleAudiobookAudio(w http.ResponseWriter, r *http.Request) {
	if a.audiobooks == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/audiobooks/audio/")
	if !audiobookAudioNameRE.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(a.audiobooks.dir, "audio", name))
}

func (a *adminServer) runAudiobookTool(call mcpToolCall) (string, error) {
	if a.audiobooks == nil {
		return "", fmt.Errorf("听书未启用，需要知识库及 audiobook.enabled")
	}
	b := a.audiobooks
	switch call.Name {
	case "list_books":
		books, err := b.catalog()
		if err != nil {
			return "", err
		}
		var out strings.Builder
		if len(books) == 0 {
			return "书架暂无已发布的书籍，请先在知识库导入并发布。", nil
		}
		for _, book := range books {
			fmt.Fprintf(&out, "%s｜《%s》｜%s｜%d章\n", book.ID, book.Title, book.Author, len(book.Chapters))
		}
		return out.String() + "用户未选书时请询问；选定后用 list_book_chapters 获取真实章节目录。", nil
	case "list_book_chapters":
		q, _ := call.Arguments["book"].(string)
		books, err := b.catalog()
		if err != nil {
			return "", err
		}
		book, err := findAudiobook(books, q)
		if err != nil {
			return "", err
		}
		// 只给目录元信息，绝不把整本书的正文返回给模型。
		data, err := json.Marshal(book)
		return string(data), err
	case "play_book":
		q, _ := call.Arguments["book"].(string)
		id, _ := call.Arguments["chapter_id"].(string)
		return b.request(q, id)
	case "control_book":
		action, _ := call.Arguments["action"].(string)
		return b.control(action)
	case "get_book_playback":
		data, err := json.Marshal(b.status())
		return string(data), err
	}
	return "", fmt.Errorf("未知听书工具")
}
