package main

// 管理台：知识库资料、导入任务、发布与试搜。
//
//	GET    /admin/api/knowledge
//	POST   /admin/api/knowledge/imports
//	GET    /admin/api/knowledge/jobs/{id}
//	GET    /admin/api/knowledge/{id}
//	PATCH  /admin/api/knowledge/{id}
//	POST   /admin/api/knowledge/{id}/publish
//	POST   /admin/api/knowledge/{id}/rollback
//	GET    /admin/api/knowledge/{id}/file
//	POST   /admin/api/knowledge/search
//	POST   /admin/api/knowledge/read

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

type knowledgeRulesResponse struct {
	Default []knowledgeRuleItem `json:"default"`
	Prompt  string              `json:"prompt_path"`
}

type knowledgeRuleItem struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func knowledgeUsageRules() knowledgeRulesResponse {
	return knowledgeRulesResponse{
		Prompt: "prompt.system",
		Default: []knowledgeRuleItem{
			{Title: "按需检索", Text: "只有用户明确求助方法、要求核对出处或原话时才查；普通闲聊、倾诉和故事不强行查资料。"},
			{Title: "先找再读", Text: "先用 search_book 找到候选，再用 read_book_section 读取原文。通常读一两个片段，失败时如实说明不确定。"},
			{Title: "自然融入", Text: "用自己的话回答，不朗读工具名、路径、行号或检索过程；用户问来源时才说资料名与章节。"},
			{Title: "建议分开", Text: "资料里的步骤与自己的补充要区分；不擅自增加练习要求，个人经验不能替代可靠的医疗建议。"},
			{Title: "停用边界", Text: "停用会在 worker 的后续检索与读取中强制执行；已经进入当前会话上下文的内容不会被追溯删除。"},
		},
	}
}

func (a *adminServer) handleKnowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	if a.knowledge == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "知识库未启用，请在配置页打开 knowledge.enabled 并重启服务",
			"sources": []any{},
			"rules":   knowledgeUsageRules(),
		})
		return
	}
	ctx, cancel := contextWithKnowledgeTimeout(r, a.knowledge)
	defer cancel()
	list, err := a.knowledge.list(ctx)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"enabled":        list.Enabled,
		"generation":     list.Generation,
		"updated_at":     list.UpdatedAt,
		"sources":        list.Sources,
		"runtime":        list.Runtime,
		"total_saved":    list.TotalSaved,
		"total_indexed":  list.TotalIndexed,
		"total_sections": list.TotalSections,
		"warning":        list.Warning,
		"rules":          knowledgeUsageRules(),
	})
}

func contextWithKnowledgeTimeout(r *http.Request, service *knowledgeService) (ctx context.Context, cancel context.CancelFunc) {
	timeout := service.timeout + time.Second
	return context.WithTimeout(r.Context(), timeout)
}

func (a *adminServer) handleKnowledgeSub(w http.ResponseWriter, r *http.Request) {
	if a.knowledge == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "知识库未启用，请在配置页打开 knowledge.enabled 并重启服务")
		return
	}
	raw := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/knowledge/"), "/")
	if raw == "" {
		a.handleKnowledge(w, r)
		return
	}
	parts := strings.Split(raw, "/")
	switch parts[0] {
	case "imports":
		a.handleKnowledgeImport(w, r)
	case "jobs":
		a.handleKnowledgeJob(w, r, parts)
	case "search":
		a.handleKnowledgeSearch(w, r)
	case "read":
		a.handleKnowledgeRead(w, r)
	default:
		a.handleKnowledgeSource(w, r, parts)
	}
}

func (a *adminServer) handleKnowledgeImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	maxBody := a.knowledge.maxUploadBytes*4 + (2 << 20)
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseMultipartForm(maxBody); err != nil {
		writeAdminError(w, http.StatusBadRequest, "上传内容过大或表单格式不正确")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	headers := r.MultipartForm.File["files"]
	if len(headers) == 0 {
		headers = r.MultipartForm.File["file"]
	}
	if len(headers) == 0 {
		writeAdminError(w, http.StatusBadRequest, "请选择 Markdown 或 TXT 文件")
		return
	}
	if len(headers) > 200 {
		writeAdminError(w, http.StatusBadRequest, "一次最多导入 200 个文件")
		return
	}
	files := make([]knowledgeUploadFile, 0, len(headers))
	for _, header := range headers {
		file, err := header.Open()
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, fmt.Sprintf("读取 %s 失败：%v", header.Filename, err))
			return
		}
		content, readErr := io.ReadAll(io.LimitReader(file, a.knowledge.maxUploadBytes+1))
		_ = file.Close()
		if readErr != nil {
			writeAdminError(w, http.StatusBadRequest, fmt.Sprintf("读取 %s 失败：%v", header.Filename, readErr))
			return
		}
		if int64(len(content)) > a.knowledge.maxUploadBytes {
			writeAdminError(w, http.StatusBadRequest, fmt.Sprintf("%s 超过 %d MB", header.Filename, a.knowledge.maxUploadBytes>>20))
			return
		}
		files = append(files, knowledgeUploadFile{FileName: header.Filename, Content: content})
	}
	job, err := a.knowledge.importText(knowledgeImportInput{
		Files:       files,
		Name:        r.FormValue("name"),
		Author:      r.FormValue("author"),
		SourceURL:   r.FormValue("source_url"),
		Description: r.FormValue("description"),
		Topics:      splitKnowledgeList(r.FormValue("topics")),
		UseCases:    splitKnowledgeList(r.FormValue("use_cases")),
		Avoid:       splitKnowledgeList(r.FormValue("avoid")),
	})
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	message := fmt.Sprintf("已导入 %d 个文件", len(files))
	published := false
	publishFailure := ""
	if r.FormValue("publish") != "false" {
		ctx, cancel := contextWithKnowledgeTimeout(r, a.knowledge)
		defer cancel()
		for attempt := 0; attempt < 3 && !published; attempt++ {
			list, listErr := a.knowledge.list(ctx)
			if listErr != nil {
				publishFailure = listErr.Error()
				break
			}
			if _, publishErr := a.knowledge.publish(job.SourceID, list.Generation, job.VersionID); publishErr == nil {
				published = true
				break
			} else if !errors.Is(publishErr, errKnowledgeConflict) {
				publishFailure = publishErr.Error()
				break
			}
		}
	}
	if published {
		message += "，已发布"
	}
	if !published {
		message += "，等待发布"
		if publishFailure != "" {
			message += "：" + publishFailure
		}
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"message":   message,
		"published": published,
		"job":       job,
	})
}

func (a *adminServer) handleKnowledgeJob(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	if len(parts) != 2 {
		writeAdminError(w, http.StatusNotFound, "没有这个导入任务")
		return
	}
	job, err := a.knowledge.job(parts[1])
	if err != nil {
		writeAdminError(w, http.StatusNotFound, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, job)
}

func (a *adminServer) handleKnowledgeSource(w http.ResponseWriter, r *http.Request, parts []string) {
	sourceID := parts[0]
	if sourceID == "" || !validKnowledgeID(sourceID) {
		writeAdminError(w, http.StatusBadRequest, "资料 ID 不合法")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			ctx, cancel := contextWithKnowledgeTimeout(r, a.knowledge)
			defer cancel()
			detail, err := a.knowledge.details(ctx, sourceID)
			if err != nil {
				writeKnowledgeError(w, err)
				return
			}
			writeAdminJSON(w, http.StatusOK, detail)
		case http.MethodPatch:
			var req knowledgePatchRequest
			r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
				return
			}
			source, err := a.knowledge.patch(sourceID, req.ExpectedGeneration, req)
			if err != nil {
				writeKnowledgeError(w, err)
				return
			}
			writeAdminJSON(w, http.StatusOK, map[string]any{
				"message": "资料设置已保存",
				"source":  source,
			})
		default:
			writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		}
		return
	}

	switch parts[1] {
	case "publish", "rollback":
		if r.Method != http.MethodPost {
			writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
			return
		}
		var req struct {
			ExpectedGeneration int64  `json:"expected_generation"`
			VersionID          string `json:"version_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
			writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
			return
		}
		source, err := a.knowledge.publish(sourceID, req.ExpectedGeneration, req.VersionID)
		if err != nil {
			writeKnowledgeError(w, err)
			return
		}
		message := "已发布，新的 worker 校验会在下一次检索前完成"
		if parts[1] == "rollback" {
			message = "已切回指定版本；旧搜索结果不会自动改写成新版"
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"message": message, "source": source})
	case "file":
		a.handleKnowledgeFile(w, r, sourceID)
	default:
		writeAdminError(w, http.StatusNotFound, "知识库接口不存在")
	}
}

func (a *adminServer) handleKnowledgeFile(w http.ResponseWriter, r *http.Request, sourceID string) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	filename := strings.TrimSpace(r.URL.Query().Get("path"))
	if filename == "" {
		writeAdminError(w, http.StatusBadRequest, "缺少 path")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	file, err := a.knowledge.readFile(sourceID, path.Clean(filename), offset)
	if err != nil {
		writeKnowledgeError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, file)
}

func (a *adminServer) handleKnowledgeSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	var req struct {
		Query    string `json:"query"`
		SourceID string `json:"source_id"`
		Limit    int    `json:"limit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeAdminError(w, http.StatusBadRequest, "请输入要检索的问题")
		return
	}
	ctx, cancel := contextWithKnowledgeTimeout(r, a.knowledge)
	defer cancel()
	result, err := a.knowledge.search(ctx, req.Query, strings.TrimSpace(req.SourceID), req.Limit)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, result)
}

func (a *adminServer) handleKnowledgeRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}
	var req struct {
		SectionID string `json:"section_id"`
		Offset    int    `json:"offset"`
		MaxChars  int    `json:"max_chars"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求 JSON 格式不正确")
		return
	}
	if strings.TrimSpace(req.SectionID) == "" {
		writeAdminError(w, http.StatusBadRequest, "缺少 section_id")
		return
	}
	ctx, cancel := contextWithKnowledgeTimeout(r, a.knowledge)
	defer cancel()
	result, err := a.knowledge.readSection(ctx, req.SectionID, req.Offset, req.MaxChars)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, result)
}

func writeKnowledgeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errKnowledgeConflict) {
		status = http.StatusConflict
	} else if strings.Contains(err.Error(), "没有") {
		status = http.StatusNotFound
	}
	writeAdminError(w, status, err.Error())
}

func splitKnowledgeList(value string) []string {
	return compactStrings(strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '、'
	}))
}
