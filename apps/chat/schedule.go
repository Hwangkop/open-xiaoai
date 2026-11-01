package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 定时任务结果播报。
//
// 任务本身由用户在豆包网页/App 里创建和维护，服务端只做只读的三件事：
//  1. 轮询 /alice/job_cron/list，用 last_run_time 判断有没有新的执行
//  2. 执行过的任务带 conversation_id，从那个会话里取最新一条 assistant 消息
//  3. 清洗成适合朗读的文本，交给音箱播报
//
// 不调用 create/update/delete：schedule 结构随版本变化，让用户在界面里配最稳。

const (
	jobStatusActive  = 1
	jobStatusDeleted = 4
)

type doubaoJob struct {
	ID             string `json:"id"`
	Type           int    `json:"type"`
	Title          string `json:"title"`
	Status         int    `json:"status"`
	LastRunTime    string `json:"last_run_time"`
	LastRunStatus  int    `json:"last_run_status"`
	ConversationID string `json:"conversation_id"`
}

type jobRecord struct {
	LastRunTime string `json:"last_run_time"`
	MessageID   string `json:"last_message_id"`
	LastText    string `json:"last_text,omitempty"`
	LastSpoken  string `json:"last_spoken_at,omitempty"`
}

type scheduleState struct {
	mu   sync.Mutex
	path string
	Jobs map[string]jobRecord `json:"jobs"`
}

func loadScheduleState(path string) *scheduleState {
	s := &scheduleState{path: path, Jobs: map[string]jobRecord{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, s); err != nil {
			log.Printf("⚠️ 读取定时任务状态失败，将从空白开始: %v", err)
		}
	}
	if s.Jobs == nil {
		s.Jobs = map[string]jobRecord{}
	}
	return s
}

func (s *scheduleState) Get(id string) (jobRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.Jobs[id]
	return rec, ok
}

func (s *scheduleState) Snapshot() map[string]jobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]jobRecord, len(s.Jobs))
	for k, v := range s.Jobs {
		out[k] = v
	}
	return out
}

func (s *scheduleState) Put(id string, rec jobRecord) {
	s.mu.Lock()
	s.Jobs[id] = rec
	s.mu.Unlock()
	s.persist()
}

func (s *scheduleState) prune(active map[string]bool) {
	s.mu.Lock()
	changed := false
	for id := range s.Jobs {
		if !active[id] {
			delete(s.Jobs, id)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.persist()
	}
}

func (s *scheduleState) persist() {
	s.mu.Lock()
	data, err := json.MarshalIndent(struct {
		Jobs map[string]jobRecord `json:"jobs"`
	}{Jobs: s.Jobs}, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("⚠️ 写入定时任务状态失败: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("⚠️ 替换定时任务状态文件失败: %v", err)
	}
}

// ScheduleWatcher 轮询豆包定时任务并把新结果排进播报队列。
type ScheduleWatcher struct {
	admin   *adminServer
	web     *DoubaoWeb
	cfg     ScheduleConfig
	speaker *Speaker
	state   *scheduleState
	loc     *time.Location

	queue chan string
	stop  chan struct{}
	once  sync.Once

	mu       sync.RWMutex
	lastErr  string
	lastPoll time.Time
}

func NewScheduleWatcher(admin *adminServer, web *DoubaoWeb, speaker *Speaker, cfg ScheduleConfig, statePath string, loc *time.Location) *ScheduleWatcher {
	return &ScheduleWatcher{
		admin:   admin,
		web:     web,
		cfg:     cfg,
		speaker: speaker,
		state:   loadScheduleState(statePath),
		loc:     loc,
		queue:   make(chan string, 8),
		stop:    make(chan struct{}),
	}
}

func (w *ScheduleWatcher) Interval() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(w.cfg.PollInterval)); err == nil && d >= 10*time.Second {
		return d
	}
	return 60 * time.Second
}

func (w *ScheduleWatcher) Start(ctx context.Context) {
	go w.playLoop(ctx)
	go w.pollLoop(ctx)
}

func (w *ScheduleWatcher) Stop() {
	w.once.Do(func() { close(w.stop) })
}

func (w *ScheduleWatcher) pollLoop(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second) // 启动后先跑一次
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-timer.C:
			w.pollOnce(ctx)
			timer.Reset(w.Interval())
		}
	}
}

func (w *ScheduleWatcher) playLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case text := <-w.queue:
			// 排队串行播放：blocking=true 保证上一句播完再播下一句
			if err := w.speaker.PlayTTS(text, true); err != nil {
				log.Printf("⚠️ 定时任务播报失败: %v", err)
			}
		}
	}
}

// Status 返回轮询状态，供管理页展示。
func (w *ScheduleWatcher) Status() (lastPoll time.Time, lastErr string) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastPoll, w.lastErr
}

func (w *ScheduleWatcher) pollOnce(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		w.lastPoll = time.Now()
		w.mu.Unlock()
	}()

	jobs, err := w.listJobs(ctx)
	if err != nil {
		w.setErr(err.Error())
		return
	}
	w.setErr("")

	active := map[string]bool{}
	for _, job := range jobs {
		if job.Status != jobStatusActive {
			continue
		}
		active[job.ID] = true

		rec, seen := w.state.Get(job.ID)
		if !seen {
			// 首次见到这个任务：记住当前进度，不补播历史
			w.state.Put(job.ID, jobRecord{
				LastRunTime: job.LastRunTime,
				MessageID:   "",
			})
			continue
		}
		if job.LastRunTime == "" || job.LastRunTime == rec.LastRunTime {
			continue
		}

		if job.ConversationID == "" {
			rec.LastRunTime = job.LastRunTime
			w.state.Put(job.ID, rec)
			continue
		}

		msg, err := w.latestAssistantMessage(ctx, job.ConversationID)
		if err != nil {
			log.Printf("⚠️ 读取任务 %s 结果失败: %v", job.Title, err)
			continue // 下轮重试
		}
		if msg == nil {
			rec.LastRunTime = job.LastRunTime
			w.state.Put(job.ID, rec)
			continue
		}
		if msg.ID == rec.MessageID {
			rec.LastRunTime = job.LastRunTime
			w.state.Put(job.ID, rec)
			continue
		}

		text := CleanForSpeech(msg.Text)
		rec.LastRunTime = job.LastRunTime
		rec.MessageID = msg.ID
		if text != "" {
			rec.LastText = text
			if w.cfg.isQuiet(time.Now(), w.loc) {
				log.Printf("🌙 静默时段，跳过播报任务 %s", job.Title)
			} else {
				select {
				case w.queue <- text:
					log.Printf("🔔 定时任务播报: %s → %q", job.Title, shortenText(text, 30))
				default:
					log.Printf("⚠️ 播报队列已满，丢弃任务 %s 的结果", job.Title)
				}
			}
		}
		w.state.Put(job.ID, rec)
	}

	w.state.prune(active)
}

func (w *ScheduleWatcher) setErr(msg string) {
	w.mu.Lock()
	w.lastErr = msg
	w.mu.Unlock()
}

type messageResult struct {
	ID   string
	Text string
}

func (w *ScheduleWatcher) listJobs(ctx context.Context) ([]doubaoJob, error) {
	if w.web == nil {
		return nil, fmt.Errorf("未启用 doubao_web")
	}
	if !w.web.LoggedIn() {
		return nil, errDoubaoNotLoggedIn
	}
	return w.web.ListJobs(ctx)
}

func (w *ScheduleWatcher) latestAssistantMessage(ctx context.Context, conversationID string) (*messageResult, error) {
	if w.web == nil {
		return nil, fmt.Errorf("未启用 doubao_web")
	}
	if !w.web.LoggedIn() {
		return nil, errDoubaoNotLoggedIn
	}
	return w.web.LatestAssistantMessage(ctx, conversationID)
}

// --- 页面脚本 ---

// 公共查询参数从页面已发出的请求里抄，避免硬编码设备信息。
const scheduleCommonPrelude = `
  const entries = performance.getEntriesByType('resource').map(e => e.name);
  const sample = entries.find(n => /\/alice\/|\/im\//.test(n));
  if (!sample) return { error: '页面还没有可复用的请求参数，请先在浏览器里打开一次豆包' };
  const qs = sample.split('?')[1] || '';
  const H = { 'Content-Type': 'application/json', 'Agw-Js-Conv': 'str', 'Accept': 'application/json, text/plain, */*' };
`

const doubaoJobListJS = `(async () => {` + scheduleCommonPrelude + `
  const res = await fetch('/alice/job_cron/list?' + qs, {
    method: 'POST', credentials: 'include', headers: H,
    body: JSON.stringify({ size: 50, sort_order: 1 })
  });
  const js = await res.json();
  if (js.code !== 0) return { error: js.msg || ('code ' + js.code) };
  const jobs = ((js.data || {}).jobs) || [];
  return { jobs: jobs.map(j => {
    const cj = ((j.content || {}).cron_job) || {};
    return {
      id: String(j.job_id || ''),
      type: j.job_type || 0,
      title: cj.title || '',
      status: cj.status || 0,
      last_run_time: String(cj.last_run_time || ''),
      last_run_status: cj.last_run_status || 0,
      conversation_id: String(j.conversation_id || '')
    };
  }) };
})()`

func doubaoLatestMessageJS(conversationID string) string {
	// conversationID 必须是纯数字，调用方已校验
	return `(async () => {` + scheduleCommonPrelude + `
  const H2 = { 'Content-Type': 'application/json; encoding=utf-8', 'Agw-Js-Conv': 'str', 'Accept': 'application/json, text/plain, */*' };
  const body = JSON.stringify({ cmd: 3100, uplink_body: { pull_singe_chain_uplink_body: {
    conversation_id: '` + conversationID + `', anchor_index: 9007199254740991, conversation_type: 3,
    direction: 1, limit: 5, ext: {}, filter: { index_list: [] },
    evaluate_ab_params: '', evaluate_common_params: '', option: { lazy_load_strategy: 0 }
  }}, sequence_id: (crypto.randomUUID ? crypto.randomUUID() : String(Date.now())), channel: 2, version: '1' });
  const res = await fetch('/im/chain/single?' + qs, { method: 'POST', credentials: 'include', headers: H2, body });
  const js = await res.json();
  const msgs = (((js.downlink_body || {}).pull_singe_chain_downlink_body || {}).messages) || [];
  let picked = null;
  for (const m of msgs) {
    if (m.user_type === 1) continue;
    const text = ((m.content_block || []).filter(b => b.block_type === 10000)
      .map(b => ((b.content || {}).text_block || {}).text || '').join('\n')).trim();
    if (!text) continue;
    if (!picked || Number(m.create_time || 0) >= Number(picked.create_time || 0)) {
      picked = { id: String(m.message_id || ''), text: text, create_time: m.create_time };
    }
  }
  if (!picked) return { has: false };
  return { has: true, id: picked.id, text: picked.text };
})()`
}

// --- 文本清洗 ---

var (
	reMarkdownLink = regexp.MustCompile(`\[([^\]]*)\]\((?:[^)]*)\)`)
	reBareURL      = regexp.MustCompile(`https?://\S+`)
	reHeading      = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s*`)
	reListMarker   = regexp.MustCompile(`(?m)^\s{0,3}[-*+]\s+`)
	reOLMarker     = regexp.MustCompile(`(?m)^\s{0,3}\d+[.)]\s+`)
	reQuote        = regexp.MustCompile(`(?m)^\s{0,3}>\s?`)
	reEmphasis     = regexp.MustCompile("[*_`~]{1,3}")
	reEmoji        = regexp.MustCompile(`[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}\x{FE0F}\x{2190}-\x{21FF}\x{2B00}-\x{2BFF}]`)
	reSpaces       = regexp.MustCompile(`[ \t]{2,}`)
	reBlankLines   = regexp.MustCompile(`\n{3,}`)
)

// CleanForSpeech 把豆包的 Markdown 回复清洗成适合 TTS 朗读的纯文本。
func CleanForSpeech(text string) string {
	s := text

	// 代码块整体丢弃（朗读代码没有意义）
	s = removeFencedCode(s)

	s = reMarkdownLink.ReplaceAllString(s, "$1")
	s = reBareURL.ReplaceAllString(s, "")
	s = reHeading.ReplaceAllString(s, "")
	s = reQuote.ReplaceAllString(s, "")
	s = reListMarker.ReplaceAllString(s, "")
	s = reOLMarker.ReplaceAllString(s, "")
	s = reEmphasis.ReplaceAllString(s, "")
	s = reEmoji.ReplaceAllString(s, "")

	// 表格分隔线、水平线
	s = regexp.MustCompile(`(?m)^\s*\|?[\s:|-]{5,}\|?\s*$`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "|", " ")

	s = reSpaces.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	s = strings.Join(lines, "\n")
	return strings.TrimSpace(s)
}

func removeFencedCode(s string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// isQuiet 判断当前是否处于静默时段（支持跨零点，如 23:00-07:00）。
func (c ScheduleConfig) isQuiet(now time.Time, loc *time.Location) bool {
	start, end, ok := parseQuietHours(c.QuietHours)
	if !ok {
		return false
	}
	if loc != nil {
		now = now.In(loc)
	}
	m := now.Hour()*60 + now.Minute()
	if start == end {
		return false
	}
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

func parseQuietHours(spec string) (start, end int, ok bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	parseOne := func(s string) (int, bool) {
		hm := strings.SplitN(strings.TrimSpace(s), ":", 2)
		if len(hm) != 2 {
			return 0, false
		}
		var h, m int
		if _, err := fmt.Sscanf(hm[0], "%d", &h); err != nil {
			return 0, false
		}
		if _, err := fmt.Sscanf(hm[1], "%d", &m); err != nil {
			return 0, false
		}
		if h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, false
		}
		return h*60 + m, true
	}
	a, ok1 := parseOne(parts[0])
	b, ok2 := parseOne(parts[1])
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return a, b, true
}

// Enqueue 把一段文本放进播报队列（供调试接口使用）。
func (w *ScheduleWatcher) Enqueue(text string) bool {
	select {
	case w.queue <- text:
		return true
	default:
		return false
	}
}

// JobsSnapshot 返回任务列表 + 本地状态，供管理页展示。
type jobView struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Status          int    `json:"status"`
	LastRunTime     string `json:"last_run_time"`
	LastRunStatus   int    `json:"last_run_status"`
	ConversationID  string `json:"conversation_id"`
	LastText        string `json:"last_text,omitempty"`
	LastSpoken      string `json:"last_spoken_at,omitempty"`
	LastRunTimeText string `json:"last_run_time_text,omitempty"`
}

func (w *ScheduleWatcher) JobsSnapshot(ctx context.Context) ([]jobView, error) {
	jobs, err := w.listJobs(ctx)
	if err != nil {
		return nil, err
	}
	state := w.state.Snapshot()
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		rec := state[j.ID]
		view := jobView{
			ID:             j.ID,
			Title:          j.Title,
			Status:         j.Status,
			LastRunTime:    j.LastRunTime,
			LastRunStatus:  j.LastRunStatus,
			ConversationID: j.ConversationID,
			LastText:       rec.LastText,
			LastSpoken:     rec.LastSpoken,
		}
		if ms, err := parseMillis(j.LastRunTime); err == nil {
			view.LastRunTimeText = time.UnixMilli(ms).Format("01-02 15:04")
		}
		out = append(out, view)
	}
	return out, nil
}

func parseMillis(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

// SpeakLatest 立即播报某个任务最近一次的结果（调试用）。
func (w *ScheduleWatcher) SpeakLatest(ctx context.Context, jobID string) (string, error) {
	jobs, err := w.listJobs(ctx)
	if err != nil {
		return "", err
	}
	for _, j := range jobs {
		if j.ID != jobID {
			continue
		}
		if j.ConversationID == "" {
			return "", fmt.Errorf("该任务还没有执行记录")
		}
		msg, err := w.latestAssistantMessage(ctx, j.ConversationID)
		if err != nil {
			return "", err
		}
		if msg == nil {
			return "", fmt.Errorf("会话里没有可播报的文本")
		}
		text := CleanForSpeech(msg.Text)
		if text == "" {
			return "", fmt.Errorf("清洗后没有可播报内容")
		}
		if !w.Enqueue(text) {
			return "", fmt.Errorf("播报队列已满")
		}
		rec, _ := w.state.Get(j.ID)
		rec.LastText = text
		rec.LastSpoken = time.Now().Format("2006-01-02 15:04:05")
		rec.MessageID = msg.ID
		rec.LastRunTime = j.LastRunTime
		w.state.Put(j.ID, rec)
		return text, nil
	}
	return "", fmt.Errorf("没找到任务 %s", jobID)
}

// PollNow 立即执行一轮轮询（调试用）。
func (w *ScheduleWatcher) PollNow(ctx context.Context) {
	w.pollOnce(ctx)
}
