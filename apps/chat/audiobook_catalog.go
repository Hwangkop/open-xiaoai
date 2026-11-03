package main

// 听书只复用知识库的已发布正文，不经由模型重写、复述或截取检索摘要。
import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const audiobookTextVersion = "plain-v1"

func bookChunkHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

type audiobookChapter struct {
	ID         string `json:"id"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	File       string `json:"file"`
	Characters int    `json:"characters"`
	Text       string `json:"-"`
}
type audiobookBook struct {
	ID       string             `json:"id"`
	Title    string             `json:"title"`
	Author   string             `json:"author"`
	Version  string             `json:"version"`
	Chapters []audiobookChapter `json:"chapters"`
}
type audiobookChunk struct {
	ChapterID    string `json:"chapter_id"`
	ChapterTitle string `json:"chapter_title"`
	Part         int    `json:"part"`
	Text         string `json:"-"`
}

var bookHeadingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
var bookSetextRE = regexp.MustCompile(`^(?:={3,}|-{3,})$`)
var bookImageRE = regexp.MustCompile(`!\[[^\]]*\]\([^\n]*?\)`)
var bookLinkRE = regexp.MustCompile(`\[([^\]]+)\]\([^\n]*?\)`)
var bookHTMLRE = regexp.MustCompile(`<[^>]*>`)
var bookURLRE = regexp.MustCompile(`https?://[^\s<>]+`)
var bookRefRE = regexp.MustCompile(`^\s*\[[^\]]+\]:\s*\S+.*$`)
var bookListRE = regexp.MustCompile(`^\s*(?:[-+*]|\d+[.)])\s+`)
var bookDigitsRE = regexp.MustCompile(`\d+|\D+`)

func bookNaturalLess(a, b string) bool {
	x, y := bookDigitsRE.FindAllString(strings.ToLower(a), -1), bookDigitsRE.FindAllString(strings.ToLower(b), -1)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] == y[i] {
			continue
		}
		n, e1 := strconv.Atoi(x[i])
		m, e2 := strconv.Atoi(y[i])
		if e1 == nil && e2 == nil && n != m {
			return n < m
		}
		return x[i] < y[i]
	}
	return len(x) < len(y)
}

// cleanBookText 只去除展示标记；图片、代码块、表格不适合原文朗读，明确跳过。
func cleanBookText(raw string) string {
	var out []string
	fence := ""
	comment := false
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			mark := t[:3]
			if fence == "" {
				fence = mark
			} else if fence == mark {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if comment {
			if strings.Contains(t, "-->") {
				comment = false
			}
			continue
		}
		if strings.Contains(t, "<!--") {
			comment = !strings.Contains(t, "-->")
			continue
		}
		if strings.HasPrefix(t, "|") || bookRefRE.MatchString(t) || bookSetextRE.MatchString(t) || t == "***" || t == "___" {
			continue
		}
		t = bookImageRE.ReplaceAllString(t, "")
		t = bookLinkRE.ReplaceAllString(t, "$1")
		t = bookHTMLRE.ReplaceAllString(t, "")
		t = bookURLRE.ReplaceAllString(t, "")
		if h := bookHeadingRE.FindStringSubmatch(t); h != nil {
			t = h[2]
		}
		t = strings.TrimLeft(t, "> ")
		t = bookListRE.ReplaceAllString(t, "")
		t = strings.NewReplacer("**", "", "__", "", "`", "", "\\*", "*").Replace(t)
		t = strings.TrimSpace(html.UnescapeString(t))
		if t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

// 多文件书籍按文件保留章界，正文里的强调标题不能变成新的章节。
// 单文件整书按顶级标题拆分，只有一个一级标题时用二级标题作为目录。
func bookFileChapters(filename, raw, version string, single bool) []audiobookChapter {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	type heading struct {
		line, level int
		title       string
	}
	var heads []heading
	fence := ""
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			m := t[:3]
			if fence == "" {
				fence = m
			} else if fence == m {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if h := bookHeadingRE.FindStringSubmatch(t); h != nil {
			heads = append(heads, heading{i, len(h[1]), cleanBookText(h[2])})
		} else if bookSetextRE.MatchString(t) && i > 0 && strings.TrimSpace(lines[i-1]) != "" {
			level := 2
			if t[0] == '=' {
				level = 1
			}
			heads = append(heads, heading{i - 1, level, cleanBookText(lines[i-1])})
		}
	}
	level := 1
	count := 0
	for _, h := range heads {
		if h.level == 1 {
			count++
		}
	}
	if count <= 1 && single {
		n := 0
		for _, h := range heads {
			if h.level == 2 {
				n++
			}
		}
		if n > 1 {
			level = 2
		}
	}
	var selected []heading
	for _, h := range heads {
		if h.level == level {
			selected = append(selected, h)
		}
	}
	if !single || len(selected) <= 1 {
		title := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
		if len(heads) > 0 {
			title = heads[0].title
		}
		selected = []heading{{0, level, title}}
	}
	var out []audiobookChapter
	// 非空前置正文也保留，不能因为拆章而悄悄截掉。
	if selected[0].line > 0 && cleanBookText(strings.Join(lines[:selected[0].line], "\n")) != "" {
		selected = append([]heading{{0, level, "开篇"}}, selected...)
	}
	for i, h := range selected {
		end := len(lines)
		if i+1 < len(selected) {
			end = selected[i+1].line
		}
		text := cleanBookText(strings.Join(lines[h.line:end], "\n"))
		if !hasSpeakableContent(text) {
			continue
		}
		sum := sha256.Sum256([]byte(version + "|" + filename + "|" + strconv.Itoa(h.line) + "|" + h.title))
		out = append(out, audiobookChapter{ID: hex.EncodeToString(sum[:12]), Title: h.title, File: filename, Characters: utf8.RuneCountInString(text), Text: text})
	}
	return out
}

func (s *knowledgeService) audiobookBooks() ([]audiobookBook, error) {
	s.mu.Lock()
	reg, err := s.loadRegistry(true)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := []audiobookBook{}
	for _, src := range reg.Sources {
		if !src.Enabled || src.Kind != "book" {
			continue
		}
		v := s.currentVersion(&src)
		if v == nil {
			continue
		}
		book, err := s.audiobookEdition(src, *v)
		if err != nil {
			return nil, fmt.Errorf("《%s》：%w", src.Name, err)
		}
		if len(book.Chapters) > 0 {
			out = append(out, book)
		}
	}
	return out, nil
}

func (s *knowledgeService) audiobookEdition(src knowledgeSource, v knowledgeVersion) (audiobookBook, error) {
	book := audiobookBook{ID: src.ID, Title: src.Name, Author: src.Author, Version: v.ID, Chapters: []audiobookChapter{}}
	manifest, err := s.readManifest(&v)
	if err != nil {
		return book, err
	}
	root, err := s.safeRootPath(v.ContentRoot)
	if err != nil {
		return book, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return book, fmt.Errorf("正文目录不可用：%w", err)
	}
	files := []string{}
	for name := range manifest.Files {
		f := fileFormat(name)
		if (f == "markdown" || f == "text") && !licenseFile(name) {
			files = append(files, name)
		}
	}
	// 多文件书籍的 README 是仓库说明或目录；单文件整书的 README 是正文。
	if len(files) > 1 {
		filtered := []string{}
		for _, f := range files {
			if !strings.EqualFold(filepath.Base(f), "README.md") && !strings.EqualFold(filepath.Base(f), "INDEX.md") {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) > 0 {
			files = filtered
		}
	}
	sort.Slice(files, func(i, j int) bool {
		rank := func(name string) int {
			base := strings.ToLower(filepath.Base(name))
			if base == "preface.md" {
				return 0
			}
			if base == "forword.md" || base == "foreword.md" {
				return 1
			}
			return 2
		}
		if x, y := rank(files[i]), rank(files[j]); x != y {
			return x < y
		}
		return bookNaturalLess(files[i], files[j])
	})
	for _, name := range files {
		target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return book, fmt.Errorf("正文 %s 不可用", name)
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return book, fmt.Errorf("正文路径越界")
		}
		info, err := os.Stat(target)
		if err != nil || info.Size() > 20<<20 {
			return book, fmt.Errorf("正文 %s 无法读取或过大", name)
		}
		data, err := os.ReadFile(target)
		if err != nil || !utf8.Valid(data) {
			return book, fmt.Errorf("正文 %s 不是可读的 UTF-8", name)
		}
		expected := manifest.Files[name]
		if expected.Bytes > 0 && expected.Bytes != int64(len(data)) {
			return book, fmt.Errorf("正文 %s 大小校验失败", name)
		}
		if expected.SHA256 != "" {
			h := sha256.Sum256(data)
			if !strings.EqualFold(hex.EncodeToString(h[:]), expected.SHA256) {
				return book, fmt.Errorf("正文 %s 校验失败", name)
			}
		}
		if expected.GitBlobSHA1 != "" {
			h := sha1.New()
			fmt.Fprintf(h, "blob %d\x00", len(data))
			h.Write(data)
			if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expected.GitBlobSHA1) {
				return book, fmt.Errorf("正文 %s 校验失败", name)
			}
		}
		book.Chapters = append(book.Chapters, bookFileChapters(name, string(data), v.ID, len(files) == 1)...)
	}
	for i := range book.Chapters {
		book.Chapters[i].Number = i + 1
	}
	return book, nil
}

// splitBookText 保留朗读文本的顺序和字符，优先在段落、句末断开，硬上限限制单次合成量。
func splitBookText(text string, limit int) []string {
	if limit < 40 {
		limit = 180
	}
	runes := []rune(strings.TrimSpace(text))
	out := []string{}
	for len(runes) > 0 {
		end := min(len(runes), limit)
		if end < len(runes) {
			for j := end - 1; j >= end/2; j-- {
				if strings.ContainsRune("。！？!?；;\n", runes[j]) {
					end = j + 1
					break
				}
			}
		}
		if part := strings.TrimSpace(string(runes[:end])); part != "" {
			out = append(out, part)
		}
		runes = runes[end:]
	}
	return out
}

func normalizeBookName(s string) string {
	s = strings.ReplaceAll(s, "当做", "当作")
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}
func findAudiobook(books []audiobookBook, query string) (audiobookBook, error) {
	q := normalizeBookName(query)
	for _, b := range books {
		if b.ID == query || normalizeBookName(b.Title) == q {
			return b, nil
		}
	}
	var found []audiobookBook
	if q != "" {
		for _, b := range books {
			if strings.Contains(normalizeBookName(b.Title), q) {
				found = append(found, b)
			}
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return audiobookBook{}, fmt.Errorf("找到多本匹配的书，请先选定书名")
	}
	return audiobookBook{}, fmt.Errorf("没有找到这本可听的书，请先列出书架")
}
