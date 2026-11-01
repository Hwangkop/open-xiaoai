package main

// 本机定时任务：任务存在 tasks.json，到点由服务端执行并直接播报。
//
// 和豆包 job_cron 的区别：
//   - 时间粒度随我们定（最低 1 分钟，豆包要求 >= 60 分钟）
//   - 结果直接在这台音箱上播，不依赖豆包推送
//   - AI 任务走 codex（可调 MCP 工具），普通提醒只播文字
//
// schedule 写法（给模型/界面共用，尽量宽松）：
//   once 2026-11-01 08:00   一次
//   daily 08:00             每天
//   weekly mon 08:00        每周（0-6 或 mon..sun，0=周日）
//   every 30m               间隔（s/m/h，最小 1m）
//   "2026-11-01 08:00"      不带前缀也当一次

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type LocalTask struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Schedule  string `json:"schedule"`
	AI        bool   `json:"ai"`       // true = 到点把 Text 交给 codex，播报回答
	Enabled   bool   `json:"enabled"`  //
	NextRun   int64  `json:"next_run"` // unix 秒
	LastRun   int64  `json:"last_run,omitempty"`
	LastText  string `json:"last_text,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// ---------- 存储 ----------

type taskStore struct {
	mu    sync.Mutex
	path  string
	loc   *time.Location
	tasks []LocalTask
}

func newTaskStore(configPath string, loc *time.Location) *taskStore {
	dir := filepath.Dir(configPath)
	if dir == "" {
		dir = "."
	}
	s := &taskStore{path: filepath.Join(dir, "tasks.json"), loc: loc}
	if loc == nil {
		s.loc = time.Local
	}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(raw, &s.tasks); err != nil {
			log.Printf("⚠️ 读取 tasks.json 失败，忽略: %v", err)
			s.tasks = nil
		}
	}
	return s
}

func (s *taskStore) saveLocked() {
	raw, err := json.MarshalIndent(s.tasks, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		log.Printf("⚠️ 写 tasks.json 失败: %v", err)
		return
	}
	_ = os.Rename(tmp, s.path)
}

// List 返回副本。
func (s *taskStore) List() []LocalTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LocalTask, len(s.tasks))
	copy(out, s.tasks)
	return out
}

func newTaskID() string {
	buf := make([]byte, 3)
	_, _ = rand.Read(buf)
	return "t" + hex.EncodeToString(buf)
}

// Add 新建任务并算出首次执行时间。
func (s *taskStore) Add(text, schedule string, ai bool) (LocalTask, error) {
	text = strings.TrimSpace(text)
	schedule = strings.TrimSpace(schedule)
	if text == "" {
		return LocalTask{}, fmt.Errorf("任务内容不能为空")
	}
	if schedule == "" {
		return LocalTask{}, fmt.Errorf("执行时间不能为空")
	}
	now := time.Now().In(s.loc)
	next, _, err := nextRun(schedule, now, s.loc)
	if err != nil {
		return LocalTask{}, err
	}

	t := LocalTask{
		ID:        newTaskID(),
		Text:      text,
		Schedule:  schedule,
		AI:        ai,
		Enabled:   true,
		NextRun:   next.Unix(),
		CreatedAt: now.Unix(),
	}
	s.mu.Lock()
	s.tasks = append(s.tasks, t)
	s.saveLocked()
	s.mu.Unlock()
	return t, nil
}

// Update 改内容或时间；空字符串表示不改。
func (s *taskStore) Update(id, text, schedule string) (LocalTask, error) {
	now := time.Now().In(s.loc)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		t := &s.tasks[i]
		if t.ID != id {
			continue
		}
		if strings.TrimSpace(text) != "" {
			t.Text = strings.TrimSpace(text)
		}
		if strings.TrimSpace(schedule) != "" {
			spec := strings.TrimSpace(schedule)
			next, _, err := nextRun(spec, now, s.loc)
			if err != nil {
				return LocalTask{}, err
			}
			t.Schedule = spec
			t.NextRun = next.Unix()
		}
		s.saveLocked()
		return *t, nil
	}
	return LocalTask{}, fmt.Errorf("任务不存在")
}

func (s *taskStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tasks {
		if t.ID == id {
			s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
			s.saveLocked()
			return true
		}
	}
	return false
}

func (s *taskStore) SetEnabled(id string, enabled bool) (LocalTask, bool) {
	now := time.Now().In(s.loc)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID != id {
			continue
		}
		s.tasks[i].Enabled = enabled
		if enabled {
			if next, _, err := nextRun(s.tasks[i].Schedule, now, s.loc); err == nil {
				s.tasks[i].NextRun = next.Unix()
			}
		}
		s.saveLocked()
		return s.tasks[i], true
	}
	return LocalTask{}, false
}

// Due 取出到点且启用中的任务（先占位，避免重复触发）。
func (s *taskStore) Due(now time.Time) []LocalTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []LocalTask
	changed := false
	for i := range s.tasks {
		t := &s.tasks[i]
		if !t.Enabled || t.NextRun == 0 || time.Unix(t.NextRun, 0).After(now) {
			continue
		}
		out = append(out, *t)
		// 先把下一次时间推后，执行失败也不会每分钟重复触发
		next, repeating, err := nextRun(t.Schedule, now, s.loc)
		if err != nil || !repeating {
			t.Enabled = false
			t.NextRun = 0
		} else {
			t.NextRun = next.Unix()
		}
		changed = true
	}
	if changed {
		s.saveLocked()
	}
	return out
}

// MarkRun 记录执行结果。
func (s *taskStore) MarkRun(id, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID == id {
			s.tasks[i].LastRun = time.Now().Unix()
			s.tasks[i].LastText = text
			s.saveLocked()
			return
		}
	}
}

func (s *taskStore) Get(id string) (LocalTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.ID == id {
			return t, true
		}
	}
	return LocalTask{}, false
}

// ---------- schedule 解析 ----------

// nextRun 计算 from 之后的下一次执行时间。
// 返回值 repeating=false 表示只执行一次（once）。
func nextRun(spec string, from time.Time, loc *time.Location) (time.Time, bool, error) {
	if loc == nil {
		loc = time.Local
	}
	spec = strings.TrimSpace(spec)
	lower := strings.ToLower(spec)
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return time.Time{}, false, fmt.Errorf("执行时间不能为空")
	}

	switch fields[0] {
	case "daily", "每天":
		hm, err := parseClock(fields[1:])
		if err != nil {
			return time.Time{}, false, err
		}
		return nextDaily(from, hm, loc), true, nil

	case "weekly", "每周":
		if len(fields) < 3 {
			return time.Time{}, false, fmt.Errorf("weekly 格式：weekly mon 08:00")
		}
		wd, err := parseWeekday(fields[1])
		if err != nil {
			return time.Time{}, false, err
		}
		hm, err := parseClock(fields[2:])
		if err != nil {
			return time.Time{}, false, err
		}
		return nextWeekly(from, wd, hm, loc), true, nil

	case "every", "每":
		return nextEvery(from, fields[1:])

	case "once", "一次":
		at, err := parseDateTime(strings.Join(fields[1:], " "), from, loc)
		if err != nil {
			return time.Time{}, false, err
		}
		if !at.After(from) {
			return time.Time{}, false, fmt.Errorf("这个时间已经过去了：%s", at.Format("2006-01-02 15:04"))
		}
		return at, false, nil
	}

	// 不带前缀：按绝对时间处理
	at, err := parseDateTime(spec, from, loc)
	if err != nil {
		return time.Time{}, false, err
	}
	if !at.After(from) {
		return time.Time{}, false, fmt.Errorf("这个时间已经过去了：%s", at.Format("2006-01-02 15:04"))
	}
	return at, false, nil
}

func parseClock(fields []string) (int, error) {
	if len(fields) == 0 {
		return 0, fmt.Errorf("缺少时间，例：08:00")
	}
	parts := strings.SplitN(fields[0], ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("时间格式应为 HH:MM")
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(strings.TrimSuffix(parts[1], ":00"))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("时间格式应为 HH:MM")
	}
	return h*60 + m, nil
}

func parseWeekday(s string) (time.Weekday, error) {
	switch strings.ToLower(s) {
	case "0", "sun", "sunday", "周日", "周天", "日":
		return time.Sunday, nil
	case "1", "mon", "monday", "周一", "一":
		return time.Monday, nil
	case "2", "tue", "tuesday", "周二", "二":
		return time.Tuesday, nil
	case "3", "wed", "wednesday", "周三", "三":
		return time.Wednesday, nil
	case "4", "thu", "thursday", "周四", "四":
		return time.Thursday, nil
	case "5", "fri", "friday", "周五", "五":
		return time.Friday, nil
	case "6", "sat", "saturday", "周六", "六":
		return time.Saturday, nil
	}
	return 0, fmt.Errorf("星期写错了吧：%s", s)
}

func nextDaily(from time.Time, minutes int, loc *time.Location) time.Time {
	now := from.In(loc)
	at := time.Date(now.Year(), now.Month(), now.Day(), minutes/60, minutes%60, 0, 0, loc)
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

func nextWeekly(from time.Time, wd time.Weekday, minutes int, loc *time.Location) time.Time {
	now := from.In(loc)
	at := time.Date(now.Year(), now.Month(), now.Day(), minutes/60, minutes%60, 0, 0, loc)
	delta := (int(wd) - int(now.Weekday()) + 7) % 7
	at = at.AddDate(0, 0, delta)
	if !at.After(now) {
		at = at.AddDate(0, 0, 7)
	}
	return at
}

func nextEvery(from time.Time, fields []string) (time.Time, bool, error) {
	if len(fields) == 0 {
		return time.Time{}, true, fmt.Errorf("every 格式：every 30m")
	}
	raw := strings.TrimSpace(fields[0])
	numText, unit := raw, ""
	for _, suffix := range []string{"秒", "s", "分", "m", "时", "h", "小时", "分钟"} {
		if strings.HasSuffix(raw, suffix) {
			numText, unit = strings.TrimSuffix(raw, suffix), suffix
			break
		}
	}
	num, err := strconv.Atoi(strings.TrimSpace(numText))
	if err != nil || num <= 0 {
		return time.Time{}, true, fmt.Errorf("间隔写错了吧：%s", raw)
	}
	var d time.Duration
	switch unit {
	case "s", "秒":
		d = time.Duration(num) * time.Second
	case "", "m", "分", "分钟":
		d = time.Duration(num) * time.Minute
	case "h", "时", "小时":
		d = time.Duration(num) * time.Hour
	default:
		return time.Time{}, true, fmt.Errorf("间隔单位只支持 s/m/h：%s", raw)
	}
	if d < time.Minute {
		return time.Time{}, true, fmt.Errorf("间隔最短 1 分钟")
	}
	return from.Add(d), true, nil
}

// parseDateTime 支持 "2026-11-01 08:00"、"2026-11-01 08:00:00"、"11-01 08:00"、"08:00"。
func parseDateTime(spec string, from time.Time, loc *time.Location) (time.Time, error) {
	spec = strings.TrimSpace(spec)
	now := from.In(loc)
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, spec, loc); err == nil {
			return t, nil
		}
	}
	// 只有时间：今天或明天
	if t, err := time.ParseInLocation("15:04", spec, loc); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	// 月日 + 时间（当年）
	if t, err := time.ParseInLocation("01-02 15:04", spec, loc); err == nil {
		at := time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !at.After(now) {
			at = at.AddDate(1, 0, 0)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("看不懂这个时间：%q（例：2026-11-01 08:00 / daily 08:00 / weekly mon 08:00 / every 30m）", spec)
}

// DescribeSchedule 给界面/模型看的人话。
func DescribeSchedule(t LocalTask) string {
	spec := strings.TrimSpace(t.Schedule)
	if t.NextRun > 0 {
		return fmt.Sprintf("%s（下次 %s）", spec, time.Unix(t.NextRun, 0).Format("01-02 15:04"))
	}
	return spec
}

// ---------- 执行器 ----------

// TaskRunner 执行 AI 任务：常驻 codex 的独立 thread，或回落 codex exec。
type TaskRunner interface {
	Ask(ctx context.Context, prompt string) (string, error)
}

type TaskScheduler struct {
	store   *taskStore
	speaker *Speaker
	runner  TaskRunner
	cfg     ScheduleConfig
	loc     *time.Location

	runMu sync.Mutex // 串行执行，避免两句话叠在一起
	stop  chan struct{}
	once  sync.Once

	mu      sync.Mutex
	running map[string]context.CancelFunc // 正在执行的任务，用于"停止"
}

func NewTaskScheduler(store *taskStore, speaker *Speaker, runner TaskRunner, cfg ScheduleConfig, loc *time.Location) *TaskScheduler {
	if loc == nil {
		loc = time.Local
	}
	return &TaskScheduler{store: store, speaker: speaker, runner: runner, cfg: cfg, loc: loc,
		stop: make(chan struct{}), running: map[string]context.CancelFunc{}}
}

func (s *TaskScheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *TaskScheduler) Stop() {
	s.once.Do(func() { close(s.stop) })
}

func (s *TaskScheduler) loop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			for _, task := range s.store.Due(time.Now().In(s.loc)) {
				go s.execute(ctx, task)
			}
		}
	}
}

// RunNow 立即执行一次（调试/界面"马上跑一次"）。
func (s *TaskScheduler) RunNow(ctx context.Context, id string) error {
	task, ok := s.store.Get(id)
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	go s.execute(ctx, task)
	return nil
}

// StopTask 停止正在执行的任务（取消 AI 生成并打断当前播报）。
func (s *TaskScheduler) StopTask(id string) bool {
	s.mu.Lock()
	cancel := s.running[id]
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	_ = s.speaker.StopTTS()
	return true
}

func (s *TaskScheduler) execute(ctx context.Context, task LocalTask) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.running[task.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, task.ID)
		s.mu.Unlock()
	}()

	text := strings.TrimSpace(task.Text)
	if task.AI {
		if s.runner == nil {
			log.Printf("⚠️ 任务 %s 需要 AI，但 codex 未配置", task.ID)
			return
		}
		now := time.Now().In(s.loc)
		prompt := fmt.Sprintf("现在是 %s。%s", now.Format("2006-01-02 15:04"), text)
		ctx2, cancel2 := context.WithTimeout(execCtx, 120*time.Second)
		defer cancel2()
		ans, err := s.runner.Ask(ctx2, prompt)
		if err != nil {
			log.Printf("⚠️ 定时任务 AI 执行失败: %v", err)
			return
		}
		text = strings.TrimSpace(ans)
	}
	if text == "" {
		return
	}
	if s.cfg.QuietHours != "" && s.cfg.isQuiet(time.Now(), s.loc) {
		log.Printf("🌙 静默时段，跳过任务 %s: %q", task.ID, shortenText(text, 30))
		s.store.MarkRun(task.ID, text)
		return
	}

	log.Printf("⏰ 定时任务执行: %s → %q", task.ID, shortenText(text, 40))
	s.speak(execCtx, text)
	s.store.MarkRun(task.ID, text)
}

// speak 按标点切句，逐句播报（和对话链路一致的断句规则）。
func (s *TaskScheduler) speak(ctx context.Context, text string) {
	rest := strings.TrimSpace(text)
	for rest != "" {
		head, tail, ok := splitAtFirstEnd(rest)
		if !ok {
			head, tail = rest, ""
		}
		rest = tail
		sentence := trimForSpeech(head)
		if !hasSpeakableContent(sentence) {
			continue
		}
		if err := s.speaker.PlayTTS(sentence, true); err != nil {
			log.Printf("⚠️ 定时任务播报失败: %v", err)
			return
		}
	}
}
