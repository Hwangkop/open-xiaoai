package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const knowledgeSchemaVersion = 1

var errKnowledgeConflict = errors.New("知识库已被其他操作更新，请刷新后重试")

type knowledgeRegistry struct {
	SchemaVersion int               `json:"schema_version"`
	Generation    int64             `json:"generation"`
	UpdatedAt     string            `json:"updated_at"`
	Sources       []knowledgeSource `json:"sources"`
}

type knowledgeSource struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Author           string             `json:"author,omitempty"`
	Kind             string             `json:"kind"`
	Description      string             `json:"description,omitempty"`
	Topics           []string           `json:"topics,omitempty"`
	UseCases         []string           `json:"use_cases,omitempty"`
	Avoid            []string           `json:"avoid,omitempty"`
	Enabled          bool               `json:"enabled"`
	PublishedVersion string             `json:"published_version,omitempty"`
	CreatedAt        string             `json:"created_at"`
	UpdatedAt        string             `json:"updated_at"`
	Versions         []knowledgeVersion `json:"versions"`
}

type knowledgeVersion struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Manifest    string `json:"manifest"`
	ContentRoot string `json:"content_root"`
	Revision    string `json:"revision,omitempty"`
	Repository  string `json:"repository,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	License     string `json:"license,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	CreatedAt   string `json:"created_at"`
	PublishedAt string `json:"published_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

type knowledgeManifest struct {
	Title      string                              `json:"title"`
	Author     string                              `json:"author"`
	Edition    string                              `json:"edition"`
	Repository string                              `json:"repository"`
	Revision   string                              `json:"revision"`
	License    string                              `json:"license"`
	SourceURL  string                              `json:"source_url"`
	Files      map[string]knowledgeFileExpectation `json:"files"`
}

type knowledgeFileExpectation struct {
	Bytes       int64  `json:"bytes"`
	GitBlobSHA1 string `json:"git_blob_sha1,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

type knowledgeService struct {
	mu             sync.Mutex
	root           string
	node           string
	script         string
	timeout        time.Duration
	maxUploadBytes int64
}

type knowledgeJob struct {
	ID         string                 `json:"id"`
	SourceID   string                 `json:"source_id"`
	VersionID  string                 `json:"version_id,omitempty"`
	Status     string                 `json:"status"`
	Phase      string                 `json:"phase"`
	Error      string                 `json:"error,omitempty"`
	Validation knowledgeJobValidation `json:"validation"`
	CreatedAt  string                 `json:"created_at"`
	UpdatedAt  string                 `json:"updated_at"`
}

type knowledgeJobValidation struct {
	SavedFiles   int `json:"saved_files"`
	IndexedFiles int `json:"indexed_files"`
}

type knowledgeImportInput struct {
	Files       []knowledgeUploadFile
	Name        string
	Author      string
	SourceURL   string
	Description string
	Topics      []string
	UseCases    []string
	Avoid       []string
}

type knowledgeUploadFile struct {
	FileName string
	Content  []byte
}

type knowledgeCoverage struct {
	SavedFiles   int `json:"saved_files"`
	IndexedFiles int `json:"indexed_files"`
	Sections     int `json:"sections"`
}

type knowledgeFileView struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Format  string `json:"format"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type knowledgeVersionView struct {
	ID            string              `json:"id"`
	Label         string              `json:"label"`
	Status        string              `json:"status"`
	Published     bool                `json:"published"`
	Revision      string              `json:"revision,omitempty"`
	Repository    string              `json:"repository,omitempty"`
	SourceURL     string              `json:"source_url,omitempty"`
	License       string              `json:"license,omitempty"`
	CreatedAt     string              `json:"created_at"`
	PublishedAt   string              `json:"published_at,omitempty"`
	SavedFiles    int                 `json:"saved_files"`
	IndexedFiles  int                 `json:"indexed_files"`
	Sections      int                 `json:"sections"`
	Error         string              `json:"error,omitempty"`
	Files         []knowledgeFileView `json:"files,omitempty"`
	ManifestTitle string              `json:"-"`
}

type knowledgeSourceView struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Author           string                 `json:"author,omitempty"`
	Kind             string                 `json:"kind"`
	Description      string                 `json:"description,omitempty"`
	Topics           []string               `json:"topics"`
	UseCases         []string               `json:"use_cases"`
	Avoid            []string               `json:"avoid"`
	Enabled          bool                   `json:"enabled"`
	PublishedVersion string                 `json:"published_version,omitempty"`
	Current          *knowledgeVersionView  `json:"current,omitempty"`
	Versions         []knowledgeVersionView `json:"versions"`
	Coverage         knowledgeCoverage      `json:"coverage"`
	CreatedAt        string                 `json:"created_at"`
	UpdatedAt        string                 `json:"updated_at"`
}

type knowledgeRuntimeView struct {
	State              string            `json:"state"`
	Message            string            `json:"message"`
	RegistryGeneration int64             `json:"registry_generation"`
	LoadedGeneration   *int64            `json:"loaded_generation,omitempty"`
	SnapshotGeneration *int64            `json:"snapshot_generation,omitempty"`
	ActiveSources      map[string]string `json:"active_sources"`
	UpdatedAt          string            `json:"updated_at,omitempty"`
	PID                int               `json:"pid,omitempty"`
	Errors             []knowledgeError  `json:"errors,omitempty"`
}

type knowledgeError struct {
	SourceID string `json:"source_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Error    string `json:"error"`
}

type knowledgeWorkerStatus struct {
	State              string            `json:"state"`
	RegistryGeneration int64             `json:"registry_generation"`
	LoadedGeneration   *int64            `json:"loaded_generation"`
	SnapshotGeneration *int64            `json:"snapshot_generation"`
	LoadedAt           string            `json:"loaded_at"`
	UpdatedAt          string            `json:"updated_at"`
	PID                int               `json:"pid"`
	ActiveSources      map[string]string `json:"active_sources"`
	Errors             []knowledgeError  `json:"errors"`
	Error              string            `json:"error"`
}

type knowledgeList struct {
	Enabled       bool                  `json:"enabled"`
	Generation    int64                 `json:"generation"`
	UpdatedAt     string                `json:"updated_at"`
	Sources       []knowledgeSourceView `json:"sources"`
	Runtime       knowledgeRuntimeView  `json:"runtime"`
	TotalSaved    int                   `json:"total_saved"`
	TotalIndexed  int                   `json:"total_indexed"`
	TotalSections int                   `json:"total_sections"`
	Warning       string                `json:"warning,omitempty"`
}

type nodeKnowledgeSources struct {
	Generation int64 `json:"generation"`
	Sources    []struct {
		ID           string `json:"id"`
		VersionID    string `json:"version_id"`
		IndexedFiles int    `json:"indexed_files"`
		SavedFiles   int    `json:"saved_files"`
		Sections     int    `json:"sections"`
	} `json:"sources"`
	Errors []knowledgeError `json:"errors"`
}

type knowledgeFileRead struct {
	SourceID    string `json:"source_id"`
	SourceTitle string `json:"source_title"`
	VersionID   string `json:"version_id"`
	Path        string `json:"path"`
	Offset      int    `json:"offset"`
	NextOffset  *int   `json:"next_offset,omitempty"`
	TotalLines  int    `json:"total_lines"`
	Text        string `json:"text"`
}

type knowledgePatchRequest struct {
	ExpectedGeneration int64     `json:"expected_generation"`
	Name               *string   `json:"name"`
	Author             *string   `json:"author"`
	Description        *string   `json:"description"`
	Topics             *[]string `json:"topics"`
	UseCases           *[]string `json:"use_cases"`
	Avoid              *[]string `json:"avoid"`
	Enabled            *bool     `json:"enabled"`
}

func newKnowledgeService(configPath string, cfg KnowledgeConfig) (*knowledgeService, error) {
	base := filepath.Dir(configPath)
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		root = "knowledge"
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(base, root)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析知识库目录失败: %w", err)
	}
	node := strings.TrimSpace(cfg.Node)
	if node == "" {
		node = "node"
	}
	script := strings.TrimSpace(cfg.Script)
	if script == "" {
		script = filepath.Join(root, "server.cjs")
	} else if !filepath.IsAbs(script) {
		script = filepath.Join(base, script)
	}
	script, err = filepath.Abs(script)
	if err != nil {
		return nil, fmt.Errorf("解析知识库 worker 路径失败: %w", err)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	maxUpload := int64(cfg.MaxUploadMB) << 20
	if maxUpload <= 0 {
		maxUpload = 20 << 20
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建知识库目录失败: %w", err)
	}
	return &knowledgeService{
		root:           root,
		node:           node,
		script:         script,
		timeout:        timeout,
		maxUploadBytes: maxUpload,
	}, nil
}

func (s *knowledgeService) registryPath() string {
	return filepath.Join(s.root, "registry.json")
}

func (s *knowledgeService) loadRegistry(create bool) (knowledgeRegistry, error) {
	data, err := os.ReadFile(s.registryPath())
	if err == nil {
		var registry knowledgeRegistry
		if err := json.Unmarshal(data, &registry); err != nil {
			return registry, fmt.Errorf("知识库登记表格式错误：%w", err)
		}
		if err := validateKnowledgeRegistry(&registry); err != nil {
			return registry, err
		}
		return registry, nil
	}
	if !os.IsNotExist(err) {
		return knowledgeRegistry{}, fmt.Errorf("读取知识库登记表失败：%w", err)
	}
	if backup, backupErr := os.ReadFile(s.registryPath() + ".bak"); backupErr == nil {
		var registry knowledgeRegistry
		if json.Unmarshal(backup, &registry) == nil && validateKnowledgeRegistry(&registry) == nil {
			if create {
				_ = s.writeRegistry(registry)
			}
			return registry, nil
		}
	}
	registry, err := s.bootstrapRegistry()
	if err != nil {
		return registry, err
	}
	if create {
		if err := s.writeRegistry(registry); err != nil {
			return registry, err
		}
	}
	return registry, nil
}

func (s *knowledgeService) bootstrapRegistry() (knowledgeRegistry, error) {
	registry := knowledgeRegistry{
		SchemaVersion: knowledgeSchemaVersion,
		Generation:    1,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Sources:       []knowledgeSource{},
	}
	booksDir := filepath.Join(s.root, "books")
	entries, err := os.ReadDir(booksDir)
	if err != nil && !os.IsNotExist(err) {
		return registry, fmt.Errorf("扫描旧知识库失败：%w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(booksDir, entry.Name(), "source.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var manifest knowledgeManifest
		if json.Unmarshal(data, &manifest) != nil {
			continue
		}
		versionID := strings.TrimSpace(manifest.Revision)
		if versionID == "" {
			versionID = "legacy"
		}
		manifestRel, _ := filepath.Rel(s.root, manifestPath)
		contentRel, _ := filepath.Rel(s.root, filepath.Join(booksDir, entry.Name(), "original"))
		registry.Sources = append(registry.Sources, knowledgeSource{
			ID:               entry.Name(),
			Name:             manifest.Title,
			Author:           manifest.Author,
			Kind:             "book",
			Enabled:          true,
			PublishedVersion: versionID,
			CreatedAt:        registry.UpdatedAt,
			UpdatedAt:        registry.UpdatedAt,
			Versions: []knowledgeVersion{{
				ID:          versionID,
				Label:       manifest.Edition,
				Status:      "ready",
				Manifest:    filepath.ToSlash(manifestRel),
				ContentRoot: filepath.ToSlash(contentRel),
				Revision:    manifest.Revision,
				Repository:  manifest.Repository,
				License:     manifest.License,
				CreatedAt:   registry.UpdatedAt,
				PublishedAt: registry.UpdatedAt,
			}},
		})
	}
	sort.Slice(registry.Sources, func(i, j int) bool {
		return registry.Sources[i].Name < registry.Sources[j].Name
	})
	return registry, nil
}

func validateKnowledgeRegistry(registry *knowledgeRegistry) error {
	if registry.SchemaVersion != knowledgeSchemaVersion {
		return fmt.Errorf("知识库 schema_version 不支持：%d", registry.SchemaVersion)
	}
	if registry.Generation < 1 {
		return fmt.Errorf("知识库 generation 不合法")
	}
	ids := map[string]bool{}
	for i := range registry.Sources {
		source := &registry.Sources[i]
		if !validKnowledgeID(source.ID) {
			return fmt.Errorf("知识库资料 ID 不合法：%q", source.ID)
		}
		if ids[source.ID] {
			return fmt.Errorf("知识库资料 ID 重复：%s", source.ID)
		}
		ids[source.ID] = true
		versionIDs := map[string]bool{}
		for _, version := range source.Versions {
			if !validKnowledgeID(version.ID) {
				return fmt.Errorf("资料 %s 的版本 ID 不合法", source.Name)
			}
			if versionIDs[version.ID] {
				return fmt.Errorf("资料 %s 的版本 ID 重复", source.Name)
			}
			versionIDs[version.ID] = true
		}
	}
	return nil
}

func validKnowledgeID(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return value != "." && value != ".."
}

func (s *knowledgeService) writeRegistry(registry knowledgeRegistry) error {
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("编码知识库登记表失败：%w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.registryPath()), 0o755); err != nil {
		return err
	}
	return replaceKnowledgeFile(s.registryPath(), data)
}

// replaceKnowledgeFile 在 Windows 上也保留一个可恢复备份；registry 是发布生效点。
func replaceKnowledgeFile(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".knowledge-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	backup := target + ".bak"
	_ = os.Remove(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, target); err != nil {
		if _, backupErr := os.Stat(backup); backupErr == nil {
			_ = os.Rename(backup, target)
		}
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func (s *knowledgeService) source(registry *knowledgeRegistry, id string) (*knowledgeSource, error) {
	for i := range registry.Sources {
		if registry.Sources[i].ID == id {
			return &registry.Sources[i], nil
		}
	}
	return nil, fmt.Errorf("没有这份资料")
}

func (s *knowledgeService) version(source *knowledgeSource, id string) (*knowledgeVersion, error) {
	for i := range source.Versions {
		if source.Versions[i].ID == id {
			return &source.Versions[i], nil
		}
	}
	return nil, fmt.Errorf("没有这个资料版本")
}

func (s *knowledgeService) currentVersion(source *knowledgeSource) *knowledgeVersion {
	if source.PublishedVersion == "" {
		return nil
	}
	version, _ := s.version(source, source.PublishedVersion)
	return version
}

func (s *knowledgeService) safeRootPath(relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("知识库路径必须是相对路径")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("知识库路径越界")
	}
	target := filepath.Join(s.root, clean)
	rel, err := filepath.Rel(s.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("知识库路径越界")
	}
	return target, nil
}

func (s *knowledgeService) readManifest(version *knowledgeVersion) (knowledgeManifest, error) {
	manifestPath, err := s.safeRootPath(version.Manifest)
	if err != nil {
		return knowledgeManifest{}, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return knowledgeManifest{}, fmt.Errorf("读取资料清单失败：%w", err)
	}
	var manifest knowledgeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return knowledgeManifest{}, fmt.Errorf("资料清单格式错误：%w", err)
	}
	if manifest.Title == "" || len(manifest.Files) == 0 {
		return knowledgeManifest{}, fmt.Errorf("资料清单缺少名称或文件")
	}
	for filename, expected := range manifest.Files {
		clean := filepath.Clean(filepath.FromSlash(filename))
		if filepath.IsAbs(filename) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return knowledgeManifest{}, fmt.Errorf("资料清单包含越界路径：%s", filename)
		}
		if expected.GitBlobSHA1 == "" && expected.SHA256 == "" {
			return knowledgeManifest{}, fmt.Errorf("资料清单缺少文件校验值：%s", filename)
		}
	}
	return manifest, nil
}

func fileFormat(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".markdown":
		return "markdown"
	case ".txt":
		return "text"
	case ".pdf":
		return "pdf"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	default:
		return "other"
	}
}

func licenseFile(filename string) bool {
	base := strings.ToLower(filepath.Base(filename))
	return strings.Contains(base, "license") || strings.Contains(base, "licence") || strings.Contains(base, "copying")
}

func (s *knowledgeService) fileViews(version *knowledgeVersion) []knowledgeFileView {
	manifest, err := s.readManifest(version)
	if err != nil {
		return []knowledgeFileView{{State: "error", Message: err.Error()}}
	}
	contentRoot, err := s.safeRootPath(version.ContentRoot)
	if err != nil {
		return []knowledgeFileView{{State: "error", Message: err.Error()}}
	}
	paths := make([]string, 0, len(manifest.Files))
	for path := range manifest.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]knowledgeFileView, 0, len(paths))
	for _, path := range paths {
		expected := manifest.Files[path]
		format := fileFormat(path)
		state := "only_saved"
		message := "已保存，未索引"
		if format == "markdown" || format == "text" {
			if licenseFile(path) {
				state = "only_saved"
				message = "许可文件，不参与全文检索"
			} else {
				state = "indexed"
				message = "已索引"
			}
		}
		if _, err := os.Stat(filepath.Join(contentRoot, filepath.FromSlash(path))); err != nil {
			state = "missing"
			message = "文件缺失"
		}
		out = append(out, knowledgeFileView{Path: path, Bytes: expected.Bytes, Format: format, State: state, Message: message})
	}
	return out
}

func (s *knowledgeService) coverage(version *knowledgeVersion) knowledgeCoverage {
	manifest, err := s.readManifest(version)
	if err != nil {
		return knowledgeCoverage{}
	}
	coverage := knowledgeCoverage{SavedFiles: len(manifest.Files)}
	for path := range manifest.Files {
		format := fileFormat(path)
		if (format == "markdown" || format == "text") && !licenseFile(path) {
			coverage.IndexedFiles++
		}
	}
	return coverage
}

func (s *knowledgeService) versionView(source knowledgeSource, version knowledgeVersion) knowledgeVersionView {
	manifest, _ := s.readManifest(&version)
	coverage := knowledgeCoverage{}
	if len(manifest.Files) > 0 {
		coverage = s.coverage(&version)
	}
	return knowledgeVersionView{
		ID:            version.ID,
		Label:         version.Label,
		Status:        version.Status,
		Published:     source.PublishedVersion == version.ID,
		Revision:      firstNonEmpty(version.Revision, manifest.Revision),
		Repository:    firstNonEmpty(version.Repository, manifest.Repository),
		SourceURL:     firstNonEmpty(version.SourceURL, manifest.SourceURL),
		License:       firstNonEmpty(version.License, manifest.License),
		CreatedAt:     version.CreatedAt,
		PublishedAt:   version.PublishedAt,
		SavedFiles:    coverage.SavedFiles,
		IndexedFiles:  coverage.IndexedFiles,
		Error:         version.Error,
		Files:         s.fileViews(&version),
		ManifestTitle: firstNonEmpty(manifest.Title, source.Name),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *knowledgeService) sourceView(source knowledgeSource, stats map[string]nodeKnowledgeSourceStat) knowledgeSourceView {
	view := knowledgeSourceView{
		ID:               source.ID,
		Name:             source.Name,
		Author:           source.Author,
		Kind:             source.Kind,
		Description:      source.Description,
		Topics:           nonNilStrings(source.Topics),
		UseCases:         nonNilStrings(source.UseCases),
		Avoid:            nonNilStrings(source.Avoid),
		Enabled:          source.Enabled,
		PublishedVersion: source.PublishedVersion,
		CreatedAt:        source.CreatedAt,
		UpdatedAt:        source.UpdatedAt,
	}
	for _, version := range source.Versions {
		versionView := s.versionView(source, version)
		key := source.ID + "@" + version.ID
		if stat, ok := stats[key]; ok {
			versionView.SavedFiles = stat.SavedFiles
			versionView.IndexedFiles = stat.IndexedFiles
			versionView.Sections = stat.Sections
		}
		view.Versions = append(view.Versions, versionView)
		if source.PublishedVersion == version.ID {
			current := versionView
			view.Current = &current
			view.Coverage = knowledgeCoverage{
				SavedFiles:   versionView.SavedFiles,
				IndexedFiles: versionView.IndexedFiles,
				Sections:     versionView.Sections,
			}
		}
	}
	if view.Versions == nil {
		view.Versions = []knowledgeVersionView{}
	}
	if view.Current == nil && len(view.Versions) > 0 {
		latest := view.Versions[len(view.Versions)-1]
		view.Coverage = knowledgeCoverage{
			SavedFiles:   latest.SavedFiles,
			IndexedFiles: latest.IndexedFiles,
			Sections:     latest.Sections,
		}
	}
	return view
}

type nodeKnowledgeSourceStat struct {
	SavedFiles   int
	IndexedFiles int
	Sections     int
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *knowledgeService) list(ctx context.Context) (knowledgeList, error) {
	s.mu.Lock()
	registry, err := s.loadRegistry(true)
	s.mu.Unlock()
	if err != nil {
		return knowledgeList{}, err
	}
	out := knowledgeList{
		Enabled:    true,
		Generation: registry.Generation,
		UpdatedAt:  registry.UpdatedAt,
		Sources:    []knowledgeSourceView{},
		Runtime:    s.runtimeStatus(registry),
	}
	stats := map[string]nodeKnowledgeSourceStat{}
	var nodeOut nodeKnowledgeSources
	if err := s.nodeRequest(ctx, map[string]any{"action": "sources"}, &nodeOut); err != nil {
		out.Warning = "检索 worker 暂时无法读取，页面显示登记信息与文件清单，片段统计待确认"
	} else {
		if nodeOut.Generation != registry.Generation {
			s.mu.Lock()
			updated, reloadErr := s.loadRegistry(true)
			s.mu.Unlock()
			if reloadErr == nil {
				registry = updated
			}
		}
		for _, item := range nodeOut.Sources {
			stats[item.ID+"@"+item.VersionID] = nodeKnowledgeSourceStat{
				SavedFiles:   item.SavedFiles,
				IndexedFiles: item.IndexedFiles,
				Sections:     item.Sections,
			}
		}
		if len(nodeOut.Errors) > 0 {
			out.Warning = "部分资料未加载：" + nodeOut.Errors[0].Error
		}
	}
	out.Generation = registry.Generation
	out.UpdatedAt = registry.UpdatedAt
	out.Runtime = s.runtimeStatus(registry)
	for _, source := range registry.Sources {
		view := s.sourceView(source, stats)
		out.Sources = append(out.Sources, view)
		out.TotalSaved += view.Coverage.SavedFiles
		out.TotalIndexed += view.Coverage.IndexedFiles
		out.TotalSections += view.Coverage.Sections
	}
	return out, nil
}

func (s *knowledgeService) details(ctx context.Context, id string) (map[string]any, error) {
	s.mu.Lock()
	registry, err := s.loadRegistry(true)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	source, err := s.source(&registry, id)
	if err != nil {
		return nil, err
	}
	stats := map[string]nodeKnowledgeSourceStat{}
	var nodeOut nodeKnowledgeSources
	if err := s.nodeRequest(ctx, map[string]any{"action": "sources"}, &nodeOut); err == nil {
		for _, item := range nodeOut.Sources {
			stats[item.ID+"@"+item.VersionID] = nodeKnowledgeSourceStat{
				SavedFiles: item.SavedFiles, IndexedFiles: item.IndexedFiles, Sections: item.Sections,
			}
		}
	}
	return map[string]any{
		"generation": registry.Generation,
		"source":     s.sourceView(*source, stats),
		"runtime":    s.runtimeStatus(registry),
	}, nil
}

func (s *knowledgeService) runtimeStatus(registry knowledgeRegistry) knowledgeRuntimeView {
	out := knowledgeRuntimeView{
		State:              "unconfirmed",
		Message:            "尚未收到 agent worker 加载确认",
		RegistryGeneration: registry.Generation,
		ActiveSources:      map[string]string{},
	}
	data, err := os.ReadFile(filepath.Join(s.root, ".runtime", "worker-status.json"))
	if err != nil {
		return out
	}
	var status knowledgeWorkerStatus
	if json.Unmarshal(data, &status) != nil {
		out.State = "error"
		out.Message = "worker 状态文件格式错误"
		return out
	}
	out.LoadedGeneration = status.LoadedGeneration
	out.SnapshotGeneration = status.SnapshotGeneration
	out.ActiveSources = status.ActiveSources
	if out.ActiveSources == nil {
		out.ActiveSources = map[string]string{}
	}
	out.UpdatedAt = status.UpdatedAt
	out.PID = status.PID
	out.Errors = status.Errors
	if updated, err := time.Parse(time.RFC3339, status.UpdatedAt); err != nil || time.Since(updated) > 60*time.Second {
		out.State = "unconfirmed"
		out.Message = "agent worker 状态已过期；启动或重启 worker 后会重新确认"
		return out
	}
	if status.State == "error" || len(status.Errors) > 0 {
		out.State = "error"
		out.Message = status.Error
		if out.Message == "" && len(status.Errors) > 0 {
			out.Message = status.Errors[0].Error
		}
		return out
	}
	if status.RegistryGeneration != registry.Generation {
		out.State = "pending"
		out.Message = "worker 尚未发现本次发布"
		return out
	}
	if status.LoadedGeneration == nil || *status.LoadedGeneration != registry.Generation {
		out.State = "pending"
		out.Message = "worker 已看到新登记表，但尚未完成有效快照加载"
		return out
	}
	out.State = "loaded"
	out.Message = "agent worker 已加载当前 generation"
	return out
}

func (s *knowledgeService) nodeRequest(ctx context.Context, payload any, out any) error {
	if info, err := os.Stat(s.script); err != nil || info.IsDir() {
		return fmt.Errorf("知识库 worker 不存在：%s", filepath.Base(s.script))
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.node, s.script, "--root", s.root, "--request")
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("知识库 worker 超时")
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("知识库 worker 执行失败：%s", message)
	}
	if err := json.Unmarshal(output, out); err != nil {
		return fmt.Errorf("知识库 worker 返回格式错误：%w", err)
	}
	return nil
}

func (s *knowledgeService) search(ctx context.Context, query, sourceID string, limit int) (json.RawMessage, error) {
	if limit < 1 || limit > 5 {
		limit = 3
	}
	var result json.RawMessage
	if err := s.nodeRequest(ctx, map[string]any{
		"action": "search", "query": query, "source_id": sourceID, "limit": limit,
	}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *knowledgeService) readSection(ctx context.Context, sectionID string, offset, maxChars int) (json.RawMessage, error) {
	var result json.RawMessage
	if err := s.nodeRequest(ctx, map[string]any{
		"action": "read", "section_id": sectionID, "offset": offset, "max_chars": maxChars,
	}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *knowledgeService) importText(input knowledgeImportInput) (knowledgeJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(input.Name) == "" {
		return knowledgeJob{}, fmt.Errorf("书名不能为空")
	}
	if len(input.Files) == 0 {
		return knowledgeJob{}, fmt.Errorf("请选择至少一个 Markdown 或 TXT 文件")
	}
	if len(input.Files) > 200 {
		return knowledgeJob{}, fmt.Errorf("一次最多导入 200 个文件")
	}
	uploaded := make(map[string]knowledgeUploadFile, len(input.Files))
	for _, file := range input.Files {
		if len(file.Content) == 0 || int64(len(file.Content)) > s.maxUploadBytes {
			return knowledgeJob{}, fmt.Errorf("%s 为空或超过 %d MB", file.FileName, s.maxUploadBytes>>20)
		}
		if !utf8.Valid(file.Content) {
			return knowledgeJob{}, fmt.Errorf("%s 不是 UTF-8 编码", file.FileName)
		}
		name := path.Base(strings.ReplaceAll(strings.TrimSpace(file.FileName), "\\", "/"))
		ext := strings.ToLower(filepath.Ext(name))
		if name == "." || name == "" || (ext != ".md" && ext != ".markdown" && ext != ".txt") {
			return knowledgeJob{}, fmt.Errorf("%s：第一期只支持 UTF-8 Markdown/TXT", file.FileName)
		}
		if _, exists := uploaded[name]; exists {
			return knowledgeJob{}, fmt.Errorf("文件名重复：%s", name)
		}
		file.FileName = name
		uploaded[name] = file
	}

	now := time.Now().UTC().Format(time.RFC3339)
	jobID := randomKnowledgeID("job")
	sourceID := randomKnowledgeID("note")
	versionID := "v1-" + jobID
	stagingRel := filepath.ToSlash(filepath.Join("sources", sourceID, "staging", jobID))
	stagingDir, _ := s.safeRootPath(stagingRel)
	originalDir := filepath.Join(stagingDir, "original")
	if err := os.MkdirAll(originalDir, 0o755); err != nil {
		return knowledgeJob{}, fmt.Errorf("创建暂存目录失败：%w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stagingDir)
		}
	}()
	manifest := knowledgeManifest{
		Title:     strings.TrimSpace(input.Name),
		Author:    strings.TrimSpace(input.Author),
		SourceURL: strings.TrimSpace(input.SourceURL),
		Files:     make(map[string]knowledgeFileExpectation, len(uploaded)),
	}
	for name, file := range uploaded {
		if err := os.WriteFile(filepath.Join(originalDir, name), file.Content, 0o644); err != nil {
			return knowledgeJob{}, fmt.Errorf("保存 %s 失败：%w", name, err)
		}
		hash := sha256.Sum256(file.Content)
		manifest.Files[name] = knowledgeFileExpectation{
			Bytes:  int64(len(file.Content)),
			SHA256: hex.EncodeToString(hash[:]),
		}
	}
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(stagingDir, "manifest.json"), manifestData, 0o644); err != nil {
		return knowledgeJob{}, fmt.Errorf("保存资料清单失败：%w", err)
	}
	version := knowledgeVersion{
		ID:          versionID,
		Label:       "第一版",
		Status:      "staged",
		Manifest:    filepath.ToSlash(filepath.Join(stagingRel, "manifest.json")),
		ContentRoot: filepath.ToSlash(filepath.Join(stagingRel, "original")),
		SourceURL:   input.SourceURL,
		JobID:       jobID,
		CreatedAt:   now,
	}
	registry, err := s.loadRegistry(true)
	if err != nil {
		return knowledgeJob{}, err
	}
	registry.Sources = append(registry.Sources, knowledgeSource{
		ID:          sourceID,
		Name:        strings.TrimSpace(input.Name),
		Author:      strings.TrimSpace(input.Author),
		Kind:        "note",
		Description: strings.TrimSpace(input.Description),
		Topics:      compactStrings(input.Topics),
		UseCases:    compactStrings(input.UseCases),
		Avoid:       compactStrings(input.Avoid),
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
		Versions:    []knowledgeVersion{version},
	})
	registry.Generation++
	registry.UpdatedAt = now
	if err := s.writeRegistry(registry); err != nil {
		return knowledgeJob{}, err
	}
	cleanup = false
	job := knowledgeJob{
		ID:        jobID,
		SourceID:  sourceID,
		VersionID: versionID,
		Status:    "completed",
		Phase:     "已校验，等待发布",
		Validation: knowledgeJobValidation{
			SavedFiles: len(uploaded), IndexedFiles: len(uploaded),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = s.saveJob(job)
	return job, nil
}

func (s *knowledgeService) saveJob(job knowledgeJob) error {
	directory := filepath.Join(s.root, ".jobs")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(job, "", "  ")
	data = append(data, '\n')
	return replaceKnowledgeFile(filepath.Join(directory, job.ID+".json"), data)
}

func (s *knowledgeService) job(id string) (knowledgeJob, error) {
	if !validKnowledgeID(id) {
		return knowledgeJob{}, fmt.Errorf("任务 ID 不合法")
	}
	data, err := os.ReadFile(filepath.Join(s.root, ".jobs", id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return knowledgeJob{}, fmt.Errorf("没有这个导入任务")
		}
		return knowledgeJob{}, err
	}
	var job knowledgeJob
	if err := json.Unmarshal(data, &job); err != nil {
		return job, fmt.Errorf("导入任务格式错误：%w", err)
	}
	return job, nil
}

func (s *knowledgeService) publish(sourceID string, expectedGeneration int64, versionID string) (knowledgeSourceView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	registry, err := s.loadRegistry(true)
	if err != nil {
		return knowledgeSourceView{}, err
	}
	if registry.Generation != expectedGeneration {
		return knowledgeSourceView{}, errKnowledgeConflict
	}
	source, err := s.source(&registry, sourceID)
	if err != nil {
		return knowledgeSourceView{}, err
	}
	if versionID == "" {
		for i := len(source.Versions) - 1; i >= 0; i-- {
			if source.Versions[i].Status == "staged" {
				versionID = source.Versions[i].ID
				break
			}
		}
	}
	version, err := s.version(source, versionID)
	if err != nil {
		return knowledgeSourceView{}, err
	}
	if version.Status != "ready" && version.Status != "staged" {
		return knowledgeSourceView{}, fmt.Errorf("版本尚未通过校验，不能发布")
	}
	if err := s.validateStoredVersion(version); err != nil {
		version.Status = "invalid"
		version.Error = err.Error()
		registry.Generation++
		registry.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		_ = s.writeRegistry(registry)
		return knowledgeSourceView{}, fmt.Errorf("版本校验失败：%w", err)
	}

	var oldManifest, oldContent, movedFrom, movedTo string
	if strings.Contains(filepath.ToSlash(version.Manifest), "/staging/") {
		manifestPath, _ := s.safeRootPath(version.Manifest)
		movedFrom = filepath.Dir(manifestPath)
		destination, _ := s.safeRootPath(filepath.ToSlash(filepath.Join("sources", sourceID, "versions", version.ID)))
		if _, err := os.Stat(destination); err == nil {
			return knowledgeSourceView{}, fmt.Errorf("版本目录已存在，拒绝覆盖：%s", version.ID)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return knowledgeSourceView{}, err
		}
		if err := os.Rename(movedFrom, destination); err != nil {
			return knowledgeSourceView{}, fmt.Errorf("提交版本目录失败：%w", err)
		}
		movedTo = destination
		oldManifest, oldContent = version.Manifest, version.ContentRoot
		version.Manifest = filepath.ToSlash(filepath.Join("sources", sourceID, "versions", version.ID, "manifest.json"))
		version.ContentRoot = filepath.ToSlash(filepath.Join("sources", sourceID, "versions", version.ID, "original"))
	}
	now := time.Now().UTC().Format(time.RFC3339)
	version.Status = "ready"
	version.Error = ""
	version.PublishedAt = now
	source.PublishedVersion = version.ID
	source.Enabled = true
	source.UpdatedAt = now
	registry.Generation++
	registry.UpdatedAt = now
	if err := s.writeRegistry(registry); err != nil {
		if movedTo != "" {
			_ = os.MkdirAll(movedFrom, 0o755)
			_ = os.Rename(movedTo, movedFrom)
		}
		version.Manifest, version.ContentRoot = oldManifest, oldContent
		return knowledgeSourceView{}, err
	}
	return s.sourceView(*source, nil), nil
}

func (s *knowledgeService) validateStoredVersion(version *knowledgeVersion) error {
	manifest, err := s.readManifest(version)
	if err != nil {
		return err
	}
	contentRoot, err := s.safeRootPath(version.ContentRoot)
	if err != nil {
		return err
	}
	for filename, expected := range manifest.Files {
		clean := filepath.Clean(filepath.FromSlash(filename))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("文件清单路径越界：%s", filename)
		}
		data, err := os.ReadFile(filepath.Join(contentRoot, clean))
		if err != nil {
			return fmt.Errorf("读取 %s 失败：%w", filename, err)
		}
		if expected.Bytes > 0 && int64(len(data)) != expected.Bytes {
			return fmt.Errorf("%s 大小校验失败", filename)
		}
		if expected.SHA256 != "" {
			hash := sha256.Sum256(data)
			if hex.EncodeToString(hash[:]) != expected.SHA256 {
				return fmt.Errorf("%s SHA256 校验失败", filename)
			}
		}
		if expected.GitBlobSHA1 != "" {
			hash := sha1.New()
			_, _ = fmt.Fprintf(hash, "blob %d\x00", len(data))
			_, _ = hash.Write(data)
			if hex.EncodeToString(hash.Sum(nil)) != expected.GitBlobSHA1 {
				return fmt.Errorf("%s Git blob SHA1 校验失败", filename)
			}
		}
	}
	return nil
}

func (s *knowledgeService) patch(sourceID string, expectedGeneration int64, patch knowledgePatchRequest) (knowledgeSourceView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	registry, err := s.loadRegistry(true)
	if err != nil {
		return knowledgeSourceView{}, err
	}
	if registry.Generation != expectedGeneration {
		return knowledgeSourceView{}, errKnowledgeConflict
	}
	source, err := s.source(&registry, sourceID)
	if err != nil {
		return knowledgeSourceView{}, err
	}
	if patch.Name != nil {
		if strings.TrimSpace(*patch.Name) == "" {
			return knowledgeSourceView{}, fmt.Errorf("资料名称不能为空")
		}
		source.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Author != nil {
		source.Author = strings.TrimSpace(*patch.Author)
	}
	if patch.Description != nil {
		source.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Topics != nil {
		source.Topics = compactStrings(*patch.Topics)
	}
	if patch.UseCases != nil {
		source.UseCases = compactStrings(*patch.UseCases)
	}
	if patch.Avoid != nil {
		source.Avoid = compactStrings(*patch.Avoid)
	}
	if patch.Enabled != nil {
		source.Enabled = *patch.Enabled
	}
	now := time.Now().UTC().Format(time.RFC3339)
	source.UpdatedAt = now
	registry.Generation++
	registry.UpdatedAt = now
	if err := s.writeRegistry(registry); err != nil {
		return knowledgeSourceView{}, err
	}
	return s.sourceView(*source, nil), nil
}

func (s *knowledgeService) readFile(sourceID, filename string, offset int) (knowledgeFileRead, error) {
	s.mu.Lock()
	registry, err := s.loadRegistry(true)
	s.mu.Unlock()
	if err != nil {
		return knowledgeFileRead{}, err
	}
	source, err := s.source(&registry, sourceID)
	if err != nil {
		return knowledgeFileRead{}, err
	}
	version := s.currentVersion(source)
	if version == nil {
		return knowledgeFileRead{}, fmt.Errorf("资料尚未发布")
	}
	manifest, err := s.readManifest(version)
	if err != nil {
		return knowledgeFileRead{}, err
	}
	filename = filepath.ToSlash(filepath.Clean(filename))
	if _, ok := manifest.Files[filename]; !ok {
		return knowledgeFileRead{}, fmt.Errorf("资料没有这个文件")
	}
	format := fileFormat(filename)
	if format != "markdown" && format != "text" {
		return knowledgeFileRead{}, fmt.Errorf("该文件已保存但当前不能按文本预览")
	}
	contentRoot, err := s.safeRootPath(version.ContentRoot)
	if err != nil {
		return knowledgeFileRead{}, err
	}
	data, err := os.ReadFile(filepath.Join(contentRoot, filepath.FromSlash(filename)))
	if err != nil {
		return knowledgeFileRead{}, fmt.Errorf("读取原文失败：%w", err)
	}
	if !utf8.Valid(data) {
		return knowledgeFileRead{}, fmt.Errorf("原文不是有效 UTF-8")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + 120
	if end > len(lines) {
		end = len(lines)
	}
	out := knowledgeFileRead{
		SourceID: sourceID, SourceTitle: source.Name, VersionID: version.ID,
		Path: filename, Offset: offset, TotalLines: len(lines),
		Text: strings.Join(lines[offset:end], "\n"),
	}
	if end < len(lines) {
		next := end
		out.NextOffset = &next
	}
	return out, nil
}

func randomKnowledgeID(prefix string) string {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
